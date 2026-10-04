import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { ApiError } from '@/api/client';
import { AuthProvider } from '@/auth/AuthContext';
import { App } from '@/App';
import '@/styles.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The WebSocket invalidates caches on real changes, so aggressive
      // polling would be duplicated work. A short stale time still covers the
      // gap while a socket is reconnecting.
      staleTime: 30_000,
      refetchOnWindowFocus: true,
      retry: (failureCount, error) => {
        // Retrying a 4xx just repeats the same rejection. Only server and
        // network failures are worth a second attempt, and only twice.
        if (error instanceof ApiError && error.status < 500) return false;
        return failureCount < 2;
      },
    },
    mutations: {
      // Mutations are never retried automatically: a "resolve ticket" that
      // times out may well have succeeded, and a blind retry would produce a
      // second audit entry for one human action.
      retry: false,
    },
  },
});

const container = document.getElementById('root');
if (!container) {
  throw new Error('#root is missing from index.html');
}

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          <App />
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
