<script lang="ts">
  import { Check, Copy } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button/index.js";

  const prompt =
    "Install Tractor from https://github.com/tylergannon/tractor, then tell me when I should start a new Codex task.";
  let copied = $state(false);

  async function copyPrompt() {
    await navigator.clipboard.writeText(prompt);
    copied = true;
    window.setTimeout(() => (copied = false), 1800);
  }
</script>

<div
  class="surface group relative overflow-hidden rounded-2xl p-1"
  data-testid="install-prompt"
>
  <div class="border-border/70 flex items-center gap-2 border-b px-4 py-3">
    <span class="size-2 rounded-full bg-red-400/70"></span>
    <span class="size-2 rounded-full bg-amber-300/70"></span>
    <span class="size-2 rounded-full bg-emerald-400/70"></span>
    <span
      class="text-muted-foreground ml-2 font-mono text-[0.65rem] tracking-[0.16em] uppercase"
    >
      Prompt your agent
    </span>
  </div>
  <div
    class="flex flex-col gap-5 p-5 sm:flex-row sm:items-start sm:justify-between sm:p-6"
  >
    <code class="text-foreground max-w-2xl text-sm leading-7 sm:text-base"
      >{prompt}</code
    >
    <Button
      variant="outline"
      size="sm"
      class="shrink-0 self-start"
      onclick={copyPrompt}
      aria-label={copied ? "Prompt copied" : "Copy install prompt"}
    >
      {#if copied}
        <Check aria-hidden="true" />
        Copied
      {:else}
        <Copy aria-hidden="true" />
        Copy
      {/if}
    </Button>
  </div>
  <p class="sr-only" aria-live="polite">
    {copied ? "Prompt copied to clipboard." : ""}
  </p>
</div>
