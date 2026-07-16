import { useState, useEffect, useRef, useCallback } from 'react'
import Markdown from 'react-markdown'

type Tab = 'nlq' | 'browse' | 'sql' | 'search'

interface Channel {
  ID: string
  Name: string
  Subscribed: boolean
}

interface SearchResult {
  Columns: string[]
  Rows: (string | number | null)[][]
}

interface GrepResult {
  Time: string
  Channel: string
  ChannelID: string
  Author: string
  Text: string
  TS: string
  ThreadTS: string
}

interface AppConfig {
  jira_url: string
  jira_projects: string[]
}

function App() {
  const [tab, setTab] = useState<Tab>('nlq')
  const [browseChannel, setBrowseChannel] = useState('')
  const [channels, setChannels] = useState<Channel[]>([])
  const [stats, setStats] = useState<{ message_count: number; channel_count: number; oldest: string; newest: string; workspace: string } | null>(null)
  const [appConfig, setAppConfig] = useState<AppConfig | null>(null)
  const [rt, setRt] = useState<{
    commit: string; go_version: string; os: string; arch: string;
    uptime_sec: number; alloc_mb: number; sys_mb: number;
    goroutines: number; gc_cycles: number; heap_objects: number;
    db_size_mb: number; last_refresh: string;
  } | null>(null)

  useEffect(() => {
    fetch('/api/channels').then(r => r.json()).then(setChannels).catch(() => {})
    fetch('/api/stats').then(r => r.json()).then(setStats).catch(() => {})
    fetch('/api/runtime').then(r => r.json()).then(setRt).catch(() => {})
    fetch('/api/config').then(r => r.json()).then(setAppConfig).catch(() => {})
  }, [])

  const tabs: { key: Tab; label: string }[] = [
    { key: 'nlq', label: '💬 Ask' },
    { key: 'browse', label: '📋 Browse' },
    { key: 'sql', label: '🛠 SQL' },
    { key: 'search', label: '🔍 Search' },
  ]

  const [sidebarOpen, setSidebarOpen] = useState(true)

  return (
    <div className="flex h-screen bg-white">
      {/* Sidebar */}
      {sidebarOpen && (
      <aside className="w-64 border-r border-gray-200 p-4 flex flex-col gap-4 overflow-y-auto shrink-0">
        <div className="flex items-center justify-between">
          <h1 className="text-xl font-bold text-gray-800">🔍 Slack Search</h1>
          <button onClick={() => setSidebarOpen(false)} className="text-gray-400 hover:text-gray-600 text-lg" title="Collapse sidebar">◀</button>
        </div>
        {stats && (
          <div className="text-xs text-gray-500 space-y-1">
            <div>{stats.message_count.toLocaleString()} messages</div>
            <div>{stats.channel_count} channels</div>
            <div>{stats.oldest} to {stats.newest}</div>
          </div>
        )}
        <hr className="border-gray-200" />
        <div className="text-sm text-gray-600 flex-1">
          <div className="font-medium mb-1">Channels</div>
          <div className="max-h-48 overflow-y-auto space-y-0.5">
            {channels.map(ch => (
              <div
                key={ch.ID}
                onClick={ch.Subscribed ? () => { setBrowseChannel(ch.Name); setTab('browse') } : undefined}
                className={`text-xs truncate ${ch.Subscribed ? 'font-bold text-gray-800 cursor-pointer hover:text-blue-600' : 'text-gray-400'}`}
              >#{ch.Name}</div>
            ))}
          </div>
        </div>
        {rt && (
          <div className="text-xs text-gray-400 space-y-0.5 pt-2 border-t border-gray-200">
            <div className="text-gray-500 font-medium text-[10px] uppercase tracking-wide mb-1">Process</div>
            <div>commit: <span className="font-mono text-gray-500">{rt.commit.slice(0, 8)}</span></div>
            <div>{rt.go_version} · {rt.os}/{rt.arch}</div>
            <div>mem: {rt.alloc_mb.toFixed(1)} MB alloc / {rt.sys_mb.toFixed(1)} MB sys</div>
            <div>goroutines: {rt.goroutines} · GC: {rt.gc_cycles}</div>
            <div>DB: {rt.db_size_mb.toFixed(1)} MB</div>
            <div>uptime: {Math.floor(rt.uptime_sec / 60)}m</div>
            {rt.last_refresh && <div>last refresh: {rt.last_refresh}</div>}
          </div>
        )}
        <div className="text-xs text-gray-400 space-y-1 pt-2 border-t border-gray-200">
          <div className="text-gray-500 font-medium">Martin Povolny</div>
          <div><a href="mailto:martin.povolny@gmail.com" className="hover:text-gray-600">martin.povolny@gmail.com</a></div>
          <div className="flex gap-3">
            <a href="https://github.com/martinpovolny" target="_blank" className="hover:text-gray-600">GitHub</a>
            <a href="https://www.linkedin.com/in/martinpovolny/" target="_blank" className="hover:text-gray-600">LinkedIn</a>
          </div>
        </div>
      </aside>
      )}
      {!sidebarOpen && (
        <div className="border-r border-gray-200 p-2 flex flex-col items-center shrink-0">
          <button onClick={() => setSidebarOpen(true)} className="text-gray-400 hover:text-gray-600 text-lg" title="Expand sidebar">▶</button>
        </div>
      )}

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
          {tab === 'nlq' && <NLQTab jiraConfig={appConfig} />}
          {tab === 'browse' && <BrowseTab channels={channels} initialChannel={browseChannel} workspace={stats?.workspace || ''} jiraConfig={appConfig} />}
          {tab === 'sql' && <SQLTab />}
          {tab === 'search' && <SearchTab />}
        </div>
      </main>
    </div>
  )
}

function ResizeDivider({ onResize }: { onResize: (delta: number) => void }) {
  const dragging = useRef(false)
  const lastX = useRef(0)

  const onMouseDown = useCallback((e: React.MouseEvent) => {
    dragging.current = true
    lastX.current = e.clientX
    e.preventDefault()

    const onMouseMove = (e: MouseEvent) => {
      if (!dragging.current) return
      const delta = e.clientX - lastX.current
      lastX.current = e.clientX
      onResize(delta)
    }
    const onMouseUp = () => {
      dragging.current = false
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
  }, [onResize])

  return (
    <div
      onMouseDown={onMouseDown}
      className="w-1 shrink-0 cursor-col-resize hover:bg-blue-300 active:bg-blue-400 bg-gray-200 transition-colors"
    />
  )
}

function VResizeDivider({ onResize }: { onResize: (delta: number) => void }) {
  const dragging = useRef(false)
  const lastY = useRef(0)

  const onMouseDown = useCallback((e: React.MouseEvent) => {
    dragging.current = true
    lastY.current = e.clientY
    e.preventDefault()

    const onMouseMove = (e: MouseEvent) => {
      if (!dragging.current) return
      const delta = e.clientY - lastY.current
      lastY.current = e.clientY
      onResize(delta)
    }
    const onMouseUp = () => {
      dragging.current = false
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    document.body.style.cursor = 'row-resize'
    document.body.style.userSelect = 'none'
  }, [onResize])

  return (
    <div
      onMouseDown={onMouseDown}
      className="h-1 shrink-0 cursor-row-resize hover:bg-blue-300 active:bg-blue-400 bg-gray-200 transition-colors"
    />
  )
}

interface ConvItem { id: string; title: string }
interface NLQResult { SQL: string; Answer: string; Result: SearchResult | null; Error: string }

function NLQTab({ jiraConfig }: { jiraConfig?: AppConfig | null }) {
  const [question, setQuestion] = useState('')
  const [loading, setLoading] = useState(false)
  const [conversations, setConversations] = useState<ConvItem[]>([])
  const [activeConv, setActiveConv] = useState<string | null>(null)
  const [messages, setMessages] = useState<{ role: string; content: string; sql?: string; result?: NLQResult }[]>([])

  useEffect(() => {
    fetch('/api/conversations').then(r => r.json()).then((c: ConvItem[]) => setConversations(c || [])).catch(() => {})
  }, [])

  const newConv = async () => {
    const resp = await fetch('/api/conversations', { method: 'POST' })
    const data = await resp.json()
    setActiveConv(data.id)
    setMessages([])
    setConversations(prev => [{ id: data.id, title: 'New conversation' }, ...prev])
  }

  const loadConv = async (id: string) => {
    setActiveConv(id)
    const resp = await fetch(`/api/conversations/${id}/messages`)
    const msgs = await resp.json()
    setMessages((msgs || []).map((m: { role: string; content: string; sql?: string; result?: SearchResult }) => ({
      role: m.role, content: m.content, sql: m.sql,
      result: m.result ? { SQL: m.sql || '', Answer: '', Result: m.result, Error: '', Mode: 'table' } as NLQResult : undefined
    })))
  }

  const deleteConv = async (id: string) => {
    await fetch(`/api/conversations/${id}`, { method: 'DELETE' })
    setConversations(prev => prev.filter(c => c.id !== id))
    if (activeConv === id) { setActiveConv(null); setMessages([]) }
  }

  const ask = async () => {
    if (!question.trim()) return
    let convId = activeConv
    if (!convId) {
      const resp = await fetch('/api/conversations', { method: 'POST' })
      const data = await resp.json()
      convId = data.id
      setActiveConv(convId)
      setConversations(prev => [{ id: convId!, title: 'New conversation' }, ...prev])
    }

    const userMsg = question
    setQuestion('')
    setMessages(prev => [...prev, { role: 'user', content: userMsg }])
    setLoading(true)

    try {
      const resp = await fetch('/api/nlq', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ question: userMsg, conversation_id: convId, max_rows: maxRows }),
      })
      const data: NLQResult = await resp.json()
      const content = data.Answer || (data.SQL ? `SQL: ${data.SQL}` : data.Error || '')
      setMessages(prev => [...prev, { role: 'assistant', content, sql: data.SQL, result: data }])
      // Refresh conversation list for title update
      fetch('/api/conversations').then(r => r.json()).then((c: ConvItem[]) => setConversations(c || [])).catch(() => {})
    } catch (e) {
      setMessages(prev => [...prev, { role: 'assistant', content: `Error: ${e}` }])
    }
    setLoading(false)
  }

  const [convWidth, setConvWidth] = useState(192)
  const [maxRows, setMaxRows] = useState(100)

  return (
    <div className="flex h-full">
      {/* Conversation list */}
      <div className="shrink-0 pr-2 space-y-2 overflow-y-auto" style={{ width: convWidth }}>
        <button onClick={newConv} className="w-full text-sm bg-gray-100 hover:bg-gray-200 rounded px-2 py-1">+ New</button>
        {conversations.map(c => (
          <div key={c.id} className={`text-xs rounded px-2 py-1 cursor-pointer ${activeConv === c.id ? 'bg-blue-100' : 'hover:bg-gray-50'}`}>
            <div className="flex items-center gap-1">
              <span className="flex-1 truncate" onClick={() => loadConv(c.id)}>{c.title}</span>
              <button onClick={() => deleteConv(c.id)} className="text-gray-400 hover:text-red-500">✕</button>
            </div>
            {activeConv === c.id && (
              <div
                className="text-[9px] font-mono text-gray-400 truncate cursor-copy hover:text-blue-500 mt-0.5"
                title="Click to copy"
                onClick={() => navigator.clipboard.writeText(c.id)}
              >{c.id}</div>
            )}
          </div>
        ))}
      </div>

      <ResizeDivider onResize={delta => setConvWidth(w => Math.max(100, Math.min(400, w + delta)))} />

      {/* Chat area */}
      <div className="flex-1 flex flex-col pl-3">
        <div className="flex-1 overflow-y-auto space-y-3 pb-4">
          {messages.map((m, i) => (
            <div key={i} className={`text-sm ${m.role === 'user' ? 'text-right' : ''}`}>
              {!(m.role === 'assistant' && m.content.startsWith('SQL: ') && m.sql) && (
                <div className={`inline-block max-w-[85%] rounded-lg px-3 py-2 ${m.role === 'user' ? 'bg-blue-500 text-white' : 'bg-gray-100 text-gray-800'}`}>
                  <MessageContent text={m.content} jiraConfig={jiraConfig} />
                </div>
              )}
              {(m.sql || m.result?.Result) && (() => {
                const hasAnswer = m.result?.Answer && m.result.Answer.length > 0
                return (
                  <div className="mt-1 space-y-1 text-xs">
                    {m.sql && (
                      <details><summary className="cursor-pointer text-gray-500">SQL</summary><pre className="mt-1 bg-gray-100 p-2 rounded overflow-x-auto">{m.sql}</pre></details>
                    )}
                    {m.result?.Result && hasAnswer && (
                      <details><summary className="cursor-pointer text-gray-500">Results ({m.result.Result.Rows?.length || 0} rows)</summary><div className="mt-1"><DataTable data={m.result.Result} /></div></details>
                    )}
                    {m.result?.Result && !hasAnswer && (
                      <div className="mt-1"><DataTable data={m.result.Result} /></div>
                    )}
                  </div>
                )
              })()}
            </div>
          ))}
          {loading && <div className="text-sm text-gray-400">Thinking…</div>}
        </div>
        <div className="flex gap-2 pt-2 border-t items-center">
          <input
            value={question}
            onChange={e => setQuestion(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && !loading && ask()}
            placeholder="Ask about your Slack archive…"
            className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          <input
            type="number"
            value={maxRows}
            onChange={e => setMaxRows(Math.max(1, parseInt(e.target.value) || 100))}
            title="Max rows sent to LLM for synthesis"
            className="w-16 border border-gray-300 rounded-lg px-2 py-2 text-sm text-center"
          />
          <button onClick={ask} disabled={loading} className="bg-blue-500 text-white px-4 py-2 rounded-lg text-sm hover:bg-blue-600 disabled:opacity-50">
            {loading ? '…' : 'Ask'}
          </button>
        </div>
      </div>
    </div>
  )
}

function slackPermalink(workspace: string, channelID: string, ts: string, threadTS?: string): string {
  const base = `https://${workspace}/archives/${channelID}/p${ts.replace('.', '')}`
  if (threadTS && threadTS !== ts) {
    return base + `?thread_ts=${threadTS}&ctype=thread`
  }
  return base
}

function BrowseTab({ channels, initialChannel, workspace, jiraConfig }: { channels: Channel[]; initialChannel?: string; workspace: string; jiraConfig?: AppConfig | null }) {
  const [text, setText] = useState('')
  const [channel, setChannel] = useState(initialChannel || '')
  const [person, setPerson] = useState('')
  const [limit, setLimit] = useState('25')
  const [results, setResults] = useState<GrepResult[]>([])
  const [selected, setSelected] = useState<GrepResult | null>(null)

  const search = async (ch?: string) => {
    const params = new URLSearchParams()
    if (text) params.set('text', text)
    const effectiveChannel = ch !== undefined ? ch : channel
    if (effectiveChannel) params.set('channel', effectiveChannel)
    if (person) params.set('person', person)
    params.set('limit', limit)
    const resp = await fetch(`/api/messages?${params}`)
    const data = await resp.json()
    setResults(data || [])
    setSelected(null)
  }

  useEffect(() => { search() }, [])

  useEffect(() => {
    if (initialChannel !== undefined && initialChannel !== channel) {
      setChannel(initialChannel)
      search(initialChannel)
    }
  }, [initialChannel])

  const [topHeight, setTopHeight] = useState(60) // percentage

  return (
    <div className="flex flex-col h-full">
      {/* Filters */}
      <div className="flex gap-2 flex-wrap items-end pb-2 shrink-0">
        <input value={text} onChange={e => setText(e.target.value)} onKeyDown={e => e.key === 'Enter' && search()} placeholder="Search text…" className="border border-gray-300 rounded px-3 py-1.5 text-sm w-48" />
        <select value={channel} onChange={e => setChannel(e.target.value)} className="border border-gray-300 rounded px-3 py-1.5 text-sm">
          <option value="">All channels</option>
          {channels.map(ch => <option key={ch.ID} value={ch.Name}>{ch.Name}</option>)}
        </select>
        <input value={person} onChange={e => setPerson(e.target.value)} placeholder="Person…" className="border border-gray-300 rounded px-3 py-1.5 text-sm w-32" />
        <select value={limit} onChange={e => setLimit(e.target.value)} className="border border-gray-300 rounded px-3 py-1.5 text-sm">
          {['25', '50', '100', '200'].map(n => <option key={n} value={n}>{n} rows</option>)}
        </select>
        <button onClick={() => search()} className="bg-blue-500 text-white px-4 py-1.5 rounded text-sm hover:bg-blue-600">Search</button>
        <span className="text-xs text-gray-500">{results.length} result(s)</span>
      </div>

      {/* Table panel */}
      <div className="overflow-auto border rounded-t-lg" style={{ height: `${topHeight}%` }}>
        <table className="w-full text-sm">
          <thead className="bg-gray-50 sticky top-0">
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

      {/* Resizable divider */}
      <VResizeDivider onResize={delta => {
        const container = document.querySelector('main > div:last-child')
        if (!container) return
        const pct = (delta / container.clientHeight) * 100
        setTopHeight(h => Math.max(20, Math.min(85, h + pct)))
      }} />

      {/* Detail panel */}
      <div className="overflow-auto border-x border-b rounded-b-lg bg-gray-50 p-4" style={{ height: `${100 - topHeight}%` }}>
        {selected ? (
          <div className="space-y-2">
            <div className="text-xs text-gray-500">
              {selected.Time} &middot; #{selected.Channel} &middot; <span className="font-medium text-gray-700">{selected.Author}</span>
              {workspace && selected.ChannelID && (
                <>
                  {' '}&middot;{' '}
                  <a href={slackPermalink(workspace, selected.ChannelID, selected.TS, selected.ThreadTS)} target="_blank" className="text-blue-500 hover:underline">Open in Slack ↗</a>
                </>
              )}
            </div>
            <div className="text-sm"><MessageContent text={selected.Text} jiraConfig={jiraConfig} highlight={text} /></div>
          </div>
        ) : (
          <div className="text-sm text-gray-400 flex items-center justify-center h-full">Click a message above to view details</div>
        )}
      </div>
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
    <div className="space-y-3">
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

interface SlackResult {
  time: string; channel: string; channel_id: string; author: string; text: string; permalink: string; ts: string
}

function SearchTab() {
  const [query, setQuery] = useState('')
  const [connected, setConnected] = useState<boolean | null>(null)
  const [results, setResults] = useState<SlackResult[]>([])
  const [selected, setSelected] = useState<SlackResult | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    fetch('/api/slack-status').then(r => r.json()).then(d => setConnected(d.connected)).catch(() => setConnected(false))
  }, [])

  const search = async () => {
    if (!query.trim()) return
    setLoading(true); setError(''); setResults([]); setSelected(null)
    try {
      const resp = await fetch('/api/slack-search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query, limit: 50 }),
      })
      const data = await resp.json()
      if (data.error) { setError(data.error) } else { setResults(data || []) }
    } catch (e) { setError(String(e)) }
    setLoading(false)
  }

  return (
    <div className="space-y-3">
      <h2 className="text-lg font-semibold text-gray-800">Slack Search</h2>
      {connected === false && (
        <div className="text-sm bg-yellow-50 border border-yellow-200 rounded-lg p-3 text-yellow-800">
          Slack credentials not loaded. Save your Chrome "Copy as cURL" to <code className="bg-yellow-100 px-1 rounded">~/.slack-search/.curl</code> and restart, or use <code className="bg-yellow-100 px-1 rounded">--curl-file path</code>.
        </div>
      )}
      <div className="flex gap-2">
        <input value={query} onChange={e => setQuery(e.target.value)} onKeyDown={e => e.key === 'Enter' && search()} placeholder="Search Slack… (supports in:#channel from:@user &quot;exact phrase&quot;)" className="flex-1 border border-gray-300 rounded-lg px-3 py-2 text-sm" />
        <button onClick={search} disabled={loading || !connected} className="bg-blue-500 text-white px-4 py-2 rounded-lg text-sm hover:bg-blue-600 disabled:opacity-50">
          {loading ? 'Searching…' : 'Search'}
        </button>
      </div>
      {error && <div className="text-red-600 text-sm bg-red-50 p-3 rounded">{error}</div>}
      {results.length > 0 && (
        <>
          <div className="text-xs text-gray-500">{results.length} result(s) — new messages cached locally</div>
          <div className="overflow-x-auto border rounded-lg">
            <table className="w-full text-sm">
              <thead className="bg-gray-50">
                <tr>
                  <th className="px-3 py-2 text-left font-medium text-gray-600">Channel</th>
                  <th className="px-3 py-2 text-left font-medium text-gray-600">Author</th>
                  <th className="px-3 py-2 text-left font-medium text-gray-600">Message</th>
                </tr>
              </thead>
              <tbody>
                {results.map((r, i) => (
                  <tr key={i} onClick={() => setSelected(r)} className={`border-t cursor-pointer hover:bg-blue-50 ${selected?.ts === r.ts ? 'bg-blue-100' : ''}`}>
                    <td className="px-3 py-1.5 text-xs">#{r.channel}</td>
                    <td className="px-3 py-1.5 text-xs font-medium">{r.author}</td>
                    <td className="px-3 py-1.5 text-xs truncate max-w-md">{r.text}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      {selected && (
        <div className="bg-gray-50 rounded-lg p-4 border space-y-2">
          <div className="text-xs text-gray-500">#{selected.channel} · <span className="font-medium text-gray-700">{selected.author}</span>
            {selected.permalink && <> · <a href={selected.permalink} target="_blank" className="text-blue-500 hover:underline">Open in Slack ↗</a></>}
          </div>
          <div className="text-sm whitespace-pre-wrap">{selected.text}</div>
        </div>
      )}
    </div>
  )
}

function linkifyJira(text: string, config: AppConfig | null): string {
  if (!config?.jira_url || !config?.jira_projects?.length) return text
  const pattern = new RegExp(`\\b(${config.jira_projects.join('|')})-(\\d+)\\b`, 'g')
  return text.replace(pattern, (m) => `[${m}](${config.jira_url}/${m})`)
}

function HighlightedText({ text, term }: { text: string; term?: string }) {
  if (!term || !text) return <>{text}</>
  try {
    const escaped = term.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const re = new RegExp(`(${escaped})`, 'gi')
    const parts = text.split(re)
    return <>{parts.map((part, i) =>
      re.test(part) ? <mark key={i} className="bg-yellow-200 px-0.5 rounded">{part}</mark> : part
    )}</>
  } catch { return <>{text}</> }
}

function MessageContent({ text, jiraConfig, highlight }: { text: string; jiraConfig?: AppConfig | null; highlight?: string }) {
  if (!text) return null
  const processed = linkifyJira(text, jiraConfig || null)
  return (
    <Markdown
      components={{
        pre: ({ children }) => <pre className="bg-gray-200 text-gray-800 p-2 rounded my-1 overflow-x-auto text-xs font-mono">{children}</pre>,
        code: ({ children, className }) => className
          ? <code className={className}>{children}</code>
          : <code className="bg-gray-200 px-1 rounded text-xs">{children}</code>,
        table: ({ children }) => <table className="border-collapse text-xs my-2 w-full">{children}</table>,
        th: ({ children }) => <th className="border border-gray-300 px-2 py-1 bg-gray-100 text-left font-medium">{children}</th>,
        td: ({ children }) => <td className="border border-gray-300 px-2 py-1">{children}</td>,
        a: ({ href, children }) => <a href={href} target="_blank" className="text-blue-500 hover:underline">{children}</a>,
        p: ({ children }) => {
          if (!highlight) return <p>{children}</p>
          return <p>{typeof children === 'string' ? <HighlightedText text={children} term={highlight} /> : children}</p>
        },
        li: ({ children }) => {
          if (!highlight) return <li>{children}</li>
          return <li>{typeof children === 'string' ? <HighlightedText text={children} term={highlight} /> : children}</li>
        },
      }}
    >{processed}</Markdown>
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
