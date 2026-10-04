import { useEffect, useRef } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { RealtimeEvent } from '@/api/types';

/**
 * Keeps the UI live by invalidating React Query caches when the server pushes
 * a ticket event.
 *
 * The deliberate choice here is that events invalidate rather than patch. A
 * patch would apply the event's payload straight into the cache — fewer
 * requests, but the client would then be reconstructing server state from a
 * partial diff, and any dropped or out-of-order event leaves the UI quietly
 * wrong. Invalidating costs one refetch and cannot desynchronise: the server
 * remains the only thing that decides what a ticket looks like.
 */
export function useRealtime(enabled: boolean): void {
  const queryClient = useQueryClient();
  const socketRef = useRef<WebSocket | null>(null);
  const reconnectAttempts = useRef(0);
  const reconnectTimer = useRef<number | null>(null);
  const closedByUs = useRef(false);

  useEffect(() => {
    if (!enabled) return;

    closedByUs.current = false;

    const connect = async (): Promise<void> => {
      try {
        // A fresh, short-lived ticket per connection. The access token never
        // goes in the URL — query strings land in proxy and server logs, and a
        // 15-minute API credential sitting in a log file is a real exposure.
        // This ticket is worthless 30 seconds later.
        const { ticket } = await api.realtimeTicket();

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const url = `${protocol}//${window.location.host}/api/v1/realtime?ticket=${encodeURIComponent(ticket)}`;
        const socket = new WebSocket(url);
        socketRef.current = socket;

        socket.onopen = () => {
          reconnectAttempts.current = 0;
        };

        socket.onmessage = (message: MessageEvent<string>) => {
          try {
            handleEvent(JSON.parse(message.data) as RealtimeEvent);
          } catch {
            // A malformed frame is not worth tearing the connection down for.
          }
        };

        socket.onclose = () => {
          socketRef.current = null;
          if (!closedByUs.current) scheduleReconnect();
        };

        socket.onerror = () => {
          socket.close();
        };
      } catch {
        scheduleReconnect();
      }
    };

    const handleEvent = (event: RealtimeEvent): void => {
      // Every event touches the ticket lists, which is where an agent is
      // usually looking.
      void queryClient.invalidateQueries({ queryKey: ['tickets'] });

      if (event.ticket_id) {
        void queryClient.invalidateQueries({ queryKey: ['ticket', event.ticket_id] });
      }
      if (event.type.startsWith('approval.')) {
        void queryClient.invalidateQueries({ queryKey: ['approvals'] });
      }
      if (event.type === 'ticket.sla_breached') {
        void queryClient.invalidateQueries({ queryKey: ['reports'] });
      }
    };

    const scheduleReconnect = (): void => {
      if (closedByUs.current) return;

      // Exponential backoff with a ceiling and jitter. Without jitter, every
      // client that dropped during a deploy reconnects on the same tick and
      // the server gets a thundering herd on the way back up.
      const attempt = Math.min(reconnectAttempts.current, 5);
      const base = Math.min(1000 * 2 ** attempt, 30_000);
      const delay = base + Math.random() * 1000;
      reconnectAttempts.current += 1;

      reconnectTimer.current = window.setTimeout(() => {
        void connect();
      }, delay);
    };

    void connect();

    return () => {
      closedByUs.current = true;
      if (reconnectTimer.current !== null) {
        window.clearTimeout(reconnectTimer.current);
      }
      socketRef.current?.close();
      socketRef.current = null;
    };
  }, [enabled, queryClient]);
}
