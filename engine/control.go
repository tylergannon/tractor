package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tylergannon/tractor/harness"
)

type runManifest struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Goal          string    `json:"goal"`
	Workdir       string    `json:"workdir"`
	StartedAt     time.Time `json:"started_at"`
	ControlSocket string    `json:"control_socket"`
}

type activeExecution struct {
	nodeID   string
	nodeType string
	stageDir string
}

type controlServer struct {
	server        *http.Server
	socketPath    string
	temporaryRoot string
}

// LoadControlSocket returns the control socket recorded for a run.
func LoadControlSocket(logsRoot string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(logsRoot, "manifest.json"))
	if err != nil {
		return "", fmt.Errorf("read run manifest: %w", err)
	}
	var manifest runManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("decode run manifest: %w", err)
	}
	if manifest.ControlSocket == "" {
		return "", errors.New("run manifest has no control socket")
	}
	return manifest.ControlSocket, nil
}

func (r *Runner) startControlServer(store *runStore) (*controlServer, runManifest, error) {
	socketPath := filepath.Join(store.root, "control.sock")
	temporaryRoot := ""
	if len(socketPath) >= 100 {
		var err error
		temporaryRoot, err = os.MkdirTemp("", "tractor-control-")
		if err != nil {
			return nil, runManifest{}, fmt.Errorf("create control socket directory: %w", err)
		}
		socketPath = filepath.Join(temporaryRoot, "control.sock")
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		if temporaryRoot != "" {
			_ = os.RemoveAll(temporaryRoot)
		}
		return nil, runManifest{}, fmt.Errorf("remove stale control socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		if temporaryRoot != "" {
			_ = os.RemoveAll(temporaryRoot)
		}
		return nil, runManifest{}, fmt.Errorf("listen on control socket: %w", err)
	}

	manifest, err := r.loadOrCreateManifest(socketPath)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		if temporaryRoot != "" {
			_ = os.RemoveAll(temporaryRoot)
		}
		return nil, runManifest{}, err
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		r.serveControl(store, response, request)
	})}
	control := &controlServer{server: server, socketPath: socketPath, temporaryRoot: temporaryRoot}
	go func() { _ = server.Serve(listener) }()
	return control, manifest, nil
}

func (r *Runner) loadOrCreateManifest(socketPath string) (runManifest, error) {
	path := filepath.Join(r.config.LogsRoot, "manifest.json")
	manifest := runManifest{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return runManifest{}, fmt.Errorf("decode run manifest: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return runManifest{}, fmt.Errorf("read run manifest: %w", err)
	}
	if manifest.ID == "" {
		id, err := newRunID()
		if err != nil {
			return runManifest{}, err
		}
		manifest.ID = id
		manifest.StartedAt = time.Now().UTC()
	}
	manifest.Name = r.graph.Name
	manifest.Goal = r.graph.Goal
	manifest.Workdir = r.config.Workdir
	manifest.ControlSocket = socketPath
	if err := writeJSON(path, manifest); err != nil {
		return runManifest{}, fmt.Errorf("write run manifest: %w", err)
	}
	return manifest, nil
}

func newRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (r *Runner) serveControl(store *runStore, response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/steer" {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var parts []harness.ContentPart
	if err := decoder.Decode(&parts); err != nil || harness.ValidateContentParts(parts) != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if r.active == nil || r.active.stageDir == "" || r.active.nodeType == "parallel" || r.config.Backend == nil {
		response.WriteHeader(http.StatusConflict)
		return
	}
	if r.config.Backend.Steer(parts) != harness.SteerAccepted {
		response.WriteHeader(http.StatusConflict)
		return
	}
	if err := store.appendSteering(r.active.stageDir, parts); err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	response.WriteHeader(http.StatusOK)
}

func (r *Runner) beginTopLevel(nodeID, nodeType string) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	r.active = &activeExecution{nodeID: nodeID, nodeType: nodeType}
}

func (r *Runner) setActiveStage(nodeID, stageDir string) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if r.active != nil && r.active.nodeID == nodeID {
		r.active.stageDir = stageDir
	}
}

func (r *Runner) clearTopLevel(nodeID string) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if r.active != nil && r.active.nodeID == nodeID {
		r.active = nil
	}
}

func (s *controlServer) close() error {
	serverErr := s.server.Close()
	removeErr := os.Remove(s.socketPath)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	var temporaryErr error
	if s.temporaryRoot != "" {
		temporaryErr = os.RemoveAll(s.temporaryRoot)
	}
	return errors.Join(serverErr, removeErr, temporaryErr)
}
