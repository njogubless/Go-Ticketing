import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Card, EmptyState, ErrorMessage, Spinner } from '@/components/primitives';

/**
 * The configuration-item inventory, plus the hotspot report.
 *
 * The hotspots panel is the reason linking tickets to assets is worth doing at
 * all: it turns a pile of individually unremarkable tickets into "these six
 * machines produced a quarter of this month's incidents", which is a
 * purchasing conversation rather than a support one.
 */
export function AssetsPage(): JSX.Element {
  const [search, setSearch] = useState('');

  const assetsQuery = useQuery({
    queryKey: ['assets', search],
    queryFn: () => api.listAssets({ ...(search ? { q: search } : {}), limit: 100 }),
  });

  const hotspotsQuery = useQuery({
    queryKey: ['assets', 'hotspots'],
    queryFn: () => api.assetHotspots(30),
    // Requesters and agents without report permission get a 403 here; the
    // query simply stays empty rather than surfacing an error they cannot act
    // on. The nav already hides what they cannot use.
    retry: false,
  });

  const assets = assetsQuery.data?.items ?? [];
  const hotspots = hotspotsQuery.data?.items ?? [];

  return (
    <>
      <header className="main__header">
        <div>
          <h1>Assets</h1>
          <div className="subtle">Equipment and services the desk supports</div>
        </div>
      </header>

      <div className="main__body">
        <div className="stack">
          {hotspots.length > 0 ? (
            <Card title="Incident hotspots — last 30 days">
              <table className="table">
                <thead>
                  <tr>
                    <th>Asset</th>
                    <th>Type</th>
                    <th>Criticality</th>
                    <th className="table__number">Incidents</th>
                  </tr>
                </thead>
                <tbody>
                  {hotspots.map(({ asset, incident_count: count }) => (
                    <tr key={asset.id}>
                      <td>
                        <span className="mono subtle">{asset.tag}</span> {asset.name}
                      </td>
                      <td className="muted">{asset.kind}</td>
                      <td>
                        <span
                          className={`badge ${
                            asset.criticality === 'high' ? 'badge--danger' : 'badge--neutral'
                          }`}
                        >
                          {asset.criticality}
                        </span>
                      </td>
                      <td className="table__number">
                        <Link to={`/queue?asset_id=${asset.id}`}>{count}</Link>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Card>
          ) : null}

          <Card
            title="Inventory"
            action={
              <input
                className="input"
                type="search"
                placeholder="Search by tag, name or serial…"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                style={{ maxWidth: 280 }}
                aria-label="Search assets"
              />
            }
          >
            {assetsQuery.isLoading ? (
              <Spinner label="Loading assets" />
            ) : assetsQuery.isError ? (
              <ErrorMessage error={assetsQuery.error} />
            ) : assets.length === 0 ? (
              <EmptyState
                title="No assets"
                description="Nothing matches that search."
              />
            ) : (
              <table className="table">
                <thead>
                  <tr>
                    <th>Tag</th>
                    <th>Name</th>
                    <th>Type</th>
                    <th>Status</th>
                    <th>Criticality</th>
                    <th>Location</th>
                  </tr>
                </thead>
                <tbody>
                  {assets.map((asset) => (
                    <tr key={asset.id}>
                      <td className="mono">{asset.tag}</td>
                      <td>{asset.name}</td>
                      <td className="muted">{asset.kind}</td>
                      <td>
                        <span
                          className={`badge ${
                            asset.status === 'retired' ? 'badge--neutral' : 'badge--info'
                          }`}
                        >
                          {asset.status.replace('_', ' ')}
                        </span>
                      </td>
                      <td>
                        <span
                          className={`badge ${
                            asset.criticality === 'high' ? 'badge--danger' : 'badge--neutral'
                          }`}
                        >
                          {asset.criticality}
                        </span>
                      </td>
                      <td className="muted">{asset.location ?? '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
        </div>
      </div>
    </>
  );
}
