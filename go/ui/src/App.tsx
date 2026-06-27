import { useState, useEffect } from 'react'

type Tab = 'nlq' | 'browse' | 'sql' | 'search'

interface Channel {
  ID: string
  Name: string
}

interface SearchResult {
  Columns: string[]
  Rows: (string | number | null)[][]
}

interface GrepResult {
  Time: string
  Channel: string
  Author: string
  Text: string
  TS: string
  ThreadTS: string
}

function App() {
  const [tab, setTab] = useState<Tab>('browse')
  const [channels, setChannels] = useState<Channel[]>([])
  const [stats, setStats] = useState<{ message_count: number; channel_count: number; oldest: string; newest: string } | null>(null)

  useEffect(() => {
    fetch('/api/channels').then(r => r.json()).then(setChannels).catch(() => {})
    fetch('/api/stats').then(r => r.json()).then(setStats).catch(() => {})
  }, [])

  const tabs: { key: Tab; label: string }[] = [
    { key: 'nlq', label: '💬 Ask' },
    { key: 'browse', label: '📋 Browse' },
    { key: 'sql', label: '🛠 SQL' },
    { key: 'search', label: '🔍 Search' },
  ]

  return (
    <div className="flex h-screen bg-white">
      {/* Sidebar */}
      <aside className="w-64 border-r border-gray-200 p-4 flex flex-col gap-4 overflow-y-auto shrink-0">
        <h1 className="text-xl font-bold text-gray-800">🔍 Slack Search</h1>
        {stats && (
          <div className="text-xs text-gray-500 space-y-1">
            <div>{stats.message_count.toLocaleString()} messages</div>
            <div>{stats.channel_count} channels</div>
            <div>{stats.oldest} to {stats.newest}</div>
          </div>
        )}
        <hr className="border-gray-200" />
        <div className="text-sm text-gray-600">
          <div className="font-medium mb-1">Channels</div>
          <div className="max-h-48 overflow-y-auto space-y-0.5">
            {channels.map(ch => (
              <div key={ch.ID} className="text-xs text-gray-500 truncate">#{ch.Name}</div>
            ))}
          </div>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 flex flex-col overflow-hidden">
        {/* Tab bar */}
        <nav className="border-b border-gray-200 px-4">
          <div className="flex gap-1">
            {tabs.map(t => (
              <button
                key={t.key}
                onClick={() => setTab(t.key)}
                className={`px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
                  tab === t.key
                    ? 'border-blue-500 text-blue-600'
                    : 'border-transparent text-gray-500 hover:text-gray-700'
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
        </nav>

        {/* Tab content */}
        <div className="flex-1 overflow-y-auto p-4">
          {tab === 'nlq' && <NLQTab />}
          {tab === 'browse' && <BrowseTab channels={channels} />}
          {tab === 'sql' && <SQLTab />}
          {tab === 'search' && <SearchTab />}
        </div>
      </main>
    </div>
  )
}

function NLQTab() {
  const [question, setQuestion] = useState('')
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<{ SQL: string; Answer: string; Result: SearchResult | null; Error: string } | null>(null)

  const ask = async () => {
    if (!question.trim()) return
    setLoading(true)
    setResult(null)
    try {
      const resp = await fetch('/api/nlq', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ question }),
      })
      const data = await resp.json()
      setResult(data)
    } catch (e) {
      setResult({ SQL: '', Answer: '', Result: null, Error: String(e) })
    }
    setLoading(false)
  }

  return (
    <div className="max-w-4xl space-y-4">
      <h2 className="text-lg font-semibold text-gray-800">Ask in Natural Language</h2>
      <div className="flex gap-2">
        <input
          value={question}
          onChange={e => setQuestion(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && ask()}
          placeholder="e.g. who sends the most messages?"
          className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <button onClick={ask} disabled={loading} className="bg-blue-500 text-white px-4 py-2 rounded-lg text-sm hover:bg-blue-600 disabled:opacity-50">
          {loading ? 'Asking…' : 'Ask'}
        </button>
      </div>
      {result && (
        <div className="space-y-3">
          {result.Error && <div className="text-red-600 text-sm bg-red-50 p-3 rounded">{result.Error}</div>}
          {result.Answer && <div className="bg-gray-50 p-4 rounded-lg text-sm whitespace-pre-wrap">{result.Answer}</div>}
          {result.SQL && (
            <details className="text-sm">
              <summary className="cursor-pointer text-gray-500">Generated SQL</summary>
              <pre className="bg-gray-100 p-3 rounded mt-1 overflow-x-auto text-xs">{result.SQL}</pre>
            </details>
          )}
          {result.Result && <DataTable data={result.Result} />}
        </div>
      )}
    </div>
  )
}

function BrowseTab({ channels }: { channels: Channel[] }) {
  const [text, setText] = useState('')
  const [channel, setChannel] = useState('')
  const [person, setPerson] = useState('')
  const [limit, setLimit] = useState('25')
  const [results, setResults] = useState<GrepResult[]>([])
  const [selected, setSelected] = useState<GrepResult | null>(null)

  const search = async () => {
    const params = new URLSearchParams()
    if (text) params.set('text', text)
    if (channel) params.set('channel', channel)
    if (person) params.set('person', person)
    params.set('limit', limit)
    const resp = await fetch(`/api/messages?${params}`)
    const data = await resp.json()
    setResults(data || [])
    setSelected(null)
  }

  useEffect(() => { search() }, [])

  return (
    <div className="space-y-3">
      <h2 className="text-lg font-semibold text-gray-800">Browse Messages</h2>
      <div className="flex gap-2 flex-wrap items-end">
        <input value={text} onChange={e => setText(e.target.value)} onKeyDown={e => e.key === 'Enter' && search()} placeholder="Search text…" className="border border-gray-300 rounded px-3 py-1.5 text-sm w-48" />
        <select value={channel} onChange={e => setChannel(e.target.value)} className="border border-gray-300 rounded px-3 py-1.5 text-sm">
          <option value="">All channels</option>
          {channels.map(ch => <option key={ch.ID} value={ch.Name}>{ch.Name}</option>)}
        </select>
        <input value={person} onChange={e => setPerson(e.target.value)} placeholder="Person…" className="border border-gray-300 rounded px-3 py-1.5 text-sm w-32" />
        <select value={limit} onChange={e => setLimit(e.target.value)} className="border border-gray-300 rounded px-3 py-1.5 text-sm">
          {['25', '50', '100', '200'].map(n => <option key={n} value={n}>{n} rows</option>)}
        </select>
        <button onClick={search} className="bg-blue-500 text-white px-4 py-1.5 rounded text-sm hover:bg-blue-600">Search</button>
      </div>

      <div className="text-xs text-gray-500">{results.length} result(s)</div>

      <div className="overflow-x-auto border rounded-lg">
        <table className="w-full text-sm">
          <thead className="bg-gray-50">
            <tr>
              <th className="px-3 py-2 text-left font-medium text-gray-600">Time</th>
              <th className="px-3 py-2 text-left font-medium text-gray-600">Channel</th>
              <th className="px-3 py-2 text-left font-medium text-gray-600">Author</th>
              <th className="px-3 py-2 text-left font-medium text-gray-600">Message</th>
            </tr>
          </thead>
          <tbody>
            {results.map((r, i) => (
              <tr
                key={i}
                onClick={() => setSelected(r)}
                className={`border-t cursor-pointer hover:bg-blue-50 ${selected?.TS === r.TS ? 'bg-blue-100' : ''}`}
              >
                <td className="px-3 py-1.5 text-xs text-gray-500 whitespace-nowrap">{r.Time}</td>
                <td className="px-3 py-1.5 text-xs text-gray-600">#{r.Channel}</td>
                <td className="px-3 py-1.5 text-xs font-medium">{r.Author}</td>
                <td className="px-3 py-1.5 text-xs truncate max-w-md">{r.Text}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {selected && (
        <div className="bg-gray-50 rounded-lg p-4 border space-y-2">
          <div className="text-xs text-gray-500">{selected.Time} &middot; #{selected.Channel} &middot; <span className="font-medium text-gray-700">{selected.Author}</span></div>
          <div className="text-sm whitespace-pre-wrap">{selected.Text}</div>
        </div>
      )}
    </div>
  )
}

function SQLTab() {
  const [query, setQuery] = useState("SELECT u.real_name, count(*) as msgs\nFROM messages m JOIN users u ON m.user_id = u.id\nGROUP BY u.id ORDER BY msgs DESC LIMIT 10")
  const [result, setResult] = useState<SearchResult | null>(null)
  const [error, setError] = useState('')

  const run = async () => {
    setError('')
    setResult(null)
    try {
      const resp = await fetch(`/api/search?q=${encodeURIComponent(query)}`)
      const data = await resp.json()
      if (data.error) { setError(data.error); return }
      setResult(data)
    } catch (e) {
      setError(String(e))
    }
  }

  return (
    <div className="space-y-3 max-w-4xl">
      <h2 className="text-lg font-semibold text-gray-800">SQL Query</h2>
      <textarea
        value={query}
        onChange={e => setQuery(e.target.value)}
        rows={5}
        className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-blue-500"
      />
      <button onClick={run} className="bg-blue-500 text-white px-4 py-2 rounded-lg text-sm hover:bg-blue-600">Run</button>
      {error && <div className="text-red-600 text-sm bg-red-50 p-3 rounded">{error}</div>}
      {result && <DataTable data={result} />}
    </div>
  )
}

function SearchTab() {
  return (
    <div className="space-y-3 max-w-4xl">
      <h2 className="text-lg font-semibold text-gray-800">Slack Search</h2>
      <p className="text-sm text-gray-500">Live search requires Slack credentials. Use the CLI: <code className="bg-gray-100 px-1 rounded">slack-search live-search --curl-file .curl "query"</code></p>
    </div>
  )
}

function DataTable({ data }: { data: SearchResult }) {
  if (!data.Rows || data.Rows.length === 0) {
    return <div className="text-sm text-gray-500">(no results)</div>
  }
  return (
    <div className="overflow-x-auto border rounded-lg">
      <table className="w-full text-sm">
        <thead className="bg-gray-50">
          <tr>
            {data.Columns.map(c => (
              <th key={c} className="px-3 py-2 text-left font-medium text-gray-600">{c}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.Rows.map((row, i) => (
            <tr key={i} className="border-t hover:bg-gray-50">
              {row.map((cell, j) => (
                <td key={j} className="px-3 py-1.5 text-xs">{cell ?? ''}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="px-3 py-2 text-xs text-gray-500 bg-gray-50 border-t">{data.Rows.length} row(s)</div>
    </div>
  )
}

export default App
