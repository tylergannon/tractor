# External steering

`external-steering.json` starts one Codex turn that waits briefly for an
external instruction. Run it with a fresh workspace and logs directory:

```sh
tractor run examples/steering/external-steering.json \
  --workdir "$WORKSPACE" --logs "$LOGS"
```

Wait until `$WORKSPACE/steering-ready.txt` exists. This marker proves the
native Codex turn is inside its 15-second foreground command. Then read
`control_socket` from `$LOGS/manifest.json` and send:

```sh
curl --silent --show-error --unix-socket "$SOCKET" \
  -H 'Content-Type: application/json' \
  --data '[{"type":"text","text":"Create steering-received.txt containing exactly TRACTOR_STEERING_RECEIVED and then finish."}]' \
  http://localhost/steer
```

Success is not the HTTP 200 alone. The live agent must create
`steering-received.txt`, the deterministic `verify` node must pass, and the
active stage must contain `steering.jsonl` with the accepted instruction.
