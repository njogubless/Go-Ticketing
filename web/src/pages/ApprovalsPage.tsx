import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card, EmptyState, ErrorMessage, Field, Spinner } from '@/components/primitives';
import { absoluteTime, relativeTime } from '@/lib/format';

/**
 * The approver's inbox for change requests.
 *
 * A rejection requires a comment and the form enforces it, because the server
 * does: a refusal with no reason leaves the requester with nothing to act on,
 * and the change simply gets resubmitted unchanged.
 */
export function ApprovalsPage(): JSX.Element {
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);

  const inboxQuery = useQuery({
    queryKey: ['approvals', 'inbox'],
    queryFn: () => api.approvalInbox(),
  });

  const decide = useMutation({
    mutationFn: (input: { id: string; decision: 'approved' | 'rejected'; comment: string }) =>
      api.decideApproval(input.id, input.decision, input.comment),
    onSuccess: () => {
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ['approvals'] });
      void queryClient.invalidateQueries({ queryKey: ['tickets'] });
    },
    onError: setError,
  });

  const approvals = inboxQuery.data?.items ?? [];

  return (
    <>
      <header className="main__header">
        <div>
          <h1>Approvals</h1>
          <div className="subtle">Changes waiting on your decision</div>
        </div>
      </header>

      <div className="main__body">
        <ErrorMessage error={error} />

        {inboxQuery.isLoading ? (
          <Spinner label="Loading approvals" />
        ) : inboxQuery.isError ? (
          <ErrorMessage error={inboxQuery.error} />
        ) : approvals.length === 0 ? (
          <Card>
            <EmptyState
              title="Nothing to approve"
              description="Change requests assigned to you will appear here."
            />
          </Card>
        ) : (
          <div className="stack" style={{ maxWidth: 720 }}>
            {approvals.map((approval) => (
              <ApprovalCard
                key={approval.id}
                approvalId={approval.id}
                requestedAt={approval.created_at}
                pending={decide.isPending}
                onDecide={(decision, comment) =>
                  decide.mutate({ id: approval.id, decision, comment })
                }
              />
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function ApprovalCard({
  approvalId,
  requestedAt,
  pending,
  onDecide,
}: {
  approvalId: string;
  requestedAt: string;
  pending: boolean;
  onDecide: (decision: 'approved' | 'rejected', comment: string) => void;
}): JSX.Element {
  const [comment, setComment] = useState('');
  const [rejecting, setRejecting] = useState(false);

  return (
    <Card
      title={`Approval request`}
      action={
        <span className="subtle" title={absoluteTime(requestedAt)}>
          asked {relativeTime(requestedAt)}
        </span>
      }
    >
      <div className="subtle mono" style={{ marginBottom: 12 }}>
        {approvalId}
      </div>

      <Field
        label={rejecting ? 'Why are you rejecting this?' : 'Comment (optional)'}
        htmlFor={`comment-${approvalId}`}
        {...(rejecting ? { hint: 'Required — the requester needs something to act on.' } : {})}
      >
        <textarea
          id={`comment-${approvalId}`}
          className="textarea"
          value={comment}
          onChange={(event) => setComment(event.target.value)}
          rows={3}
        />
      </Field>

      <div className="row">
        <button
          type="button"
          className="button button--primary"
          disabled={pending}
          onClick={() => onDecide('approved', comment)}
        >
          Approve
        </button>
        <button
          type="button"
          className="button button--danger"
          disabled={pending || (rejecting && !comment.trim())}
          onClick={() => {
            if (!rejecting) {
              setRejecting(true);
              return;
            }
            onDecide('rejected', comment);
          }}
        >
          {rejecting ? 'Confirm rejection' : 'Reject'}
        </button>
        {rejecting ? (
          <button type="button" className="button button--ghost" onClick={() => setRejecting(false)}>
            Cancel
          </button>
        ) : null}
        <span className="spacer" />
        <Link to="/queue?preset=approvals" className="button button--ghost button--sm">
          View changes
        </Link>
      </div>
    </Card>
  );
}
