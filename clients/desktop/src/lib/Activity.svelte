<script lang="ts">
  // Phase FF (v0.10.98): in-app activity log. Renders the buffered SSE
  // events from stores.svelte.ts::events (capped at 200). For deeper
  // forensics users go to Console.app via Settings → About → View logs
  // (Phase GG). This view is for "what's been happening recently in
  // hopssh's day-to-day" — connect/disconnect, watchdog auto-recovery,
  // peer transitions, etc.
  import { agent } from './stores.svelte';
  import type { LocalEvent } from './local-api';

  // Filter UI state.
  let filter = $state<'all' | 'connection' | 'enrollment' | 'recovery'>('all');

  // Group event types into user-friendly buckets so the filter buttons
  // map to "what kind of activity is this" rather than the raw type
  // strings. Each bucket name is plain English (NN/g jargon rule).
  const buckets: Record<string, 'connection' | 'enrollment' | 'recovery' | 'other'> = {
    'status': 'connection',
    'enrollment.added': 'enrollment',
    'enrollment.removed': 'enrollment',
    'instance.watchdog-tripped': 'recovery',
    'instance.watchdog-recovered': 'recovery',
    'instance.watchdog-recovery-failed': 'recovery',
    'instance.watchdog-cooldown': 'recovery',
  };

  function bucketFor(ev: LocalEvent): string {
    return buckets[ev.type] ?? 'other';
  }

  let visible = $derived(
    filter === 'all'
      ? agent.events
      : agent.events.filter((ev) => bucketFor(ev) === filter)
  );

  // Format ISO timestamp to a compact "HH:MM:SS · today/yesterday/date"
  // form. Avoids importing a date lib for one display.
  function formatTime(iso: string): string {
    try {
      const d = new Date(iso);
      const now = new Date();
      const sameDay =
        d.getFullYear() === now.getFullYear() &&
        d.getMonth() === now.getMonth() &&
        d.getDate() === now.getDate();
      const time = d.toLocaleTimeString(undefined, {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      });
      if (sameDay) return time;
      return `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} · ${time}`;
    } catch {
      return iso;
    }
  }

  // Friendly label for an event. Most events have a `name` field
  // (enrollment) we surface; some carry their own context that's
  // useful in the headline.
  function summarize(ev: LocalEvent): string {
    const name = (ev.data?.name as string | undefined) ?? '';
    switch (ev.type) {
      case 'status':
        return 'Status updated';
      case 'enrollment.added':
        return name ? `Enrolled in "${name}"` : 'Network added';
      case 'enrollment.removed':
        return name ? `Removed "${name}"` : 'Network removed';
      case 'instance.watchdog-tripped':
        return name
          ? `${name}: connection issue — recovering automatically`
          : 'Connection issue — recovering automatically';
      case 'instance.watchdog-recovered':
        return name ? `${name}: connection restored` : 'Connection restored';
      case 'instance.watchdog-recovery-failed': {
        const err = (ev.data?.error as string | undefined) ?? 'unknown reason';
        return name ? `${name}: couldn't recover — ${err}` : `Couldn't recover — ${err}`;
      }
      case 'instance.watchdog-cooldown':
        return name
          ? `${name}: recovery throttled (cooldown)`
          : 'Recovery throttled (cooldown)';
      default:
        return ev.type;
    }
  }

  function severity(ev: LocalEvent): 'info' | 'warn' | 'error' {
    switch (ev.type) {
      case 'instance.watchdog-tripped':
      case 'instance.watchdog-cooldown':
        return 'warn';
      case 'instance.watchdog-recovery-failed':
        return 'error';
      default:
        return 'info';
    }
  }

  function dotClass(sev: 'info' | 'warn' | 'error'): string {
    if (sev === 'error') return 'bg-red-500';
    if (sev === 'warn') return 'bg-amber-400';
    return 'bg-zinc-500';
  }
</script>

<div class="flex h-full flex-col">
  <div class="flex shrink-0 items-center justify-between border-b border-zinc-800 px-4 py-2.5">
    <h2 class="text-base font-semibold">Activity</h2>
    <div class="flex items-center gap-1 text-[11px]">
      <button
        class={filter === 'all'
          ? 'rounded-md bg-zinc-800 px-2 py-0.5 text-zinc-100'
          : 'rounded-md px-2 py-0.5 text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200'}
        onclick={() => (filter = 'all')}
      >
        All
      </button>
      <button
        class={filter === 'connection'
          ? 'rounded-md bg-zinc-800 px-2 py-0.5 text-zinc-100'
          : 'rounded-md px-2 py-0.5 text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200'}
        onclick={() => (filter = 'connection')}
        title="Status updates"
      >
        Connection
      </button>
      <button
        class={filter === 'enrollment'
          ? 'rounded-md bg-zinc-800 px-2 py-0.5 text-zinc-100'
          : 'rounded-md px-2 py-0.5 text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200'}
        onclick={() => (filter = 'enrollment')}
        title="Networks added or removed"
      >
        Networks
      </button>
      <button
        class={filter === 'recovery'
          ? 'rounded-md bg-zinc-800 px-2 py-0.5 text-zinc-100'
          : 'rounded-md px-2 py-0.5 text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200'}
        onclick={() => (filter = 'recovery')}
        title="Watchdog auto-recovery events"
      >
        Recovery
      </button>
    </div>
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto">
    {#if visible.length === 0}
      <div class="px-4 py-8 text-center text-xs text-zinc-500">
        {#if filter === 'all'}
          Nothing's happened yet. Activity from this session will show up here.
        {:else}
          No {filter} events yet.
        {/if}
      </div>
    {:else}
      <ul class="divide-y divide-zinc-800">
        {#each visible as ev}
          {@const sev = severity(ev)}
          <li class="flex items-start gap-3 px-4 py-2.5 text-xs">
            <span class={`mt-1.5 inline-block h-1.5 w-1.5 shrink-0 rounded-full ${dotClass(sev)}`}></span>
            <div class="min-w-0 flex-1">
              <div class="font-medium text-zinc-200">{summarize(ev)}</div>
              <div class="mt-0.5 text-[10px] text-zinc-500">{formatTime(ev.time)}</div>
            </div>
          </li>
        {/each}
      </ul>
      <div class="px-4 py-3 text-center text-[10px] text-zinc-600">
        Showing the last {visible.length} of up to 200 events from this session.
        For deeper forensics, see Settings → About → View agent logs.
      </div>
    {/if}
  </div>
</div>
