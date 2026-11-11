import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { content, type CardIcon, type Locale, type SiteContent } from './content'
import { buildInstallCommand, publishedStableTag, stableReleaseApi } from './release'

const githubUrl = 'https://github.com/routegate/RouteGate'
const docsUrl = `${githubUrl}/tree/main/docs`
const installGuideUrl = `${githubUrl}/blob/main/docs/guides/first-install.md`
const releasesUrl = `${githubUrl}/releases`
const securityUrl = `${githubUrl}/blob/main/SECURITY.md`
const licenseUrl = `${githubUrl}/blob/main/LICENSE`
const sourceCodeUrl = `${githubUrl}/blob/main/agent/internal/diagnostics/diagnostics.go#L22-L35`
const assetUrl = (path: string) => `${import.meta.env.BASE_URL}${path.replace(/^\//, '')}`

type StableRelease = { status: 'loading' | 'ready' | 'unavailable'; tag: string | null }

function readStoredLocale(): Locale | null {
  try {
    const savedLocale = window.localStorage.getItem('routegate-locale')
    return savedLocale === 'ru' || savedLocale === 'en' ? savedLocale : null
  } catch {
    return null
  }
}

function persistLocale(locale: Locale) {
  try {
    window.localStorage.setItem('routegate-locale', locale)
  } catch {
    // Storage can be unavailable in restricted/private browsing contexts.
  }
}

async function copyText(value: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value)
    return
  }

  const textarea = document.createElement('textarea')
  textarea.value = value
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  textarea.select()
  document.execCommand('copy')
  textarea.remove()
}

function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <span className={`brand ${compact ? 'brand--compact' : ''}`}>
      <img src={assetUrl('routegate-symbol.svg')} alt="" />
      <span>RouteGate</span>
    </span>
  )
}

function Icon({ name }: { name: CardIcon }) {
  const paths: Record<CardIcon, ReactNode> = {
    server: <><rect x="4" y="5" width="16" height="6" rx="2" /><rect x="4" y="13" width="16" height="6" rx="2" /><path d="M8 8h.01M8 16h.01M12 8h5M12 16h5" /></>,
    protocol: <><circle cx="6" cy="12" r="2.5" /><circle cx="18" cy="7" r="2.5" /><circle cx="18" cy="17" r="2.5" /><path d="M8.5 11l7-3M8.5 13l7 3" /></>,
    devices: <><rect x="3" y="4" width="13" height="10" rx="2" /><path d="M7 18h5M9.5 14v4" /><rect x="17" y="8" width="4" height="9" rx="1" /></>,
    route: <><circle cx="6" cy="17" r="2" /><circle cx="18" cy="7" r="2" /><path d="M8 17h2a4 4 0 0 0 4-4v-2a4 4 0 0 1 4-4" /></>,
    manager: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M7 8h4M7 12h10M7 16h7" /></>,
    agent: <><circle cx="7" cy="12" r="3" /><circle cx="17" cy="7" r="2" /><circle cx="17" cy="17" r="2" /><path d="M10 11l5-3M10 13l5 3" /></>,
    runtime: <><rect x="4" y="5" width="16" height="14" rx="2" /><path d="M8 9h8M8 13h5M17 13h.01" /></>,
    access: <><circle cx="9" cy="9" r="3" /><path d="M4 20c.5-3.5 2.2-5 5-5 2.1 0 3.6.8 4.5 2.5M15 11l2 2 4-4" /></>,
  }
  return <svg viewBox="0 0 24 24" aria-hidden="true">{paths[name]}</svg>
}

function projectMapPoint(longitude: number, latitude: number) {
  return {
    x: ((longitude + 180) / 360) * 100,
    y: ((90 - latitude) / 180) * 100,
  }
}

function routePath(from: { x: number; y: number }, to: { x: number; y: number }) {
  const controlX = (from.x + to.x) / 2
  const lift = Math.min(8, Math.max(2.5, Math.abs(to.x - from.x) * 0.08))
  const controlY = ((from.y + to.y) / 2) - lift

  return `M${from.x.toFixed(2)} ${from.y.toFixed(2)} Q${controlX.toFixed(2)} ${controlY.toFixed(2)} ${to.x.toFixed(2)} ${to.y.toFixed(2)}`
}

function HeroMapPreview({ locale }: { locale: Locale }) {
  const isEnglish = locale === 'en'
  const nodes = [
    { name: 'New York', longitude: -74.0060, latitude: 40.7128, hub: true },
    { name: 'Frankfurt', longitude: 8.6821, latitude: 50.1109 },
    { name: 'Helsinki', longitude: 24.9384, latitude: 60.1699 },
    { name: 'Moscow', longitude: 37.6173, latitude: 55.7558 },
    { name: 'Singapore', longitude: 103.8198, latitude: 1.3521 },
    { name: 'Tokyo', longitude: 139.6503, latitude: 35.6762 },
  ].map(node => ({ ...node, ...projectMapPoint(node.longitude, node.latitude) }))
  const hub = nodes.find(node => node.hub) ?? nodes[0]
  const routes = nodes.filter(node => !node.hub).map(node => routePath(hub, node))
  const metrics = isEnglish
    ? [
        ['Managed nodes', '6 / 6', 'online'],
        ['Connected agents', '6 / 6', 'healthy'],
        ['User access', 'Ready', 'delivery'],
        ['Configuration', 'Applied', 'verified'],
      ]
    : [
        ['Управляемые узлы', '6 / 6', 'онлайн'],
        ['Подключённые агенты', '6 / 6', 'исправны'],
        ['Доступ пользователей', 'Готов', 'выдача'],
        ['Конфигурация', 'Применена', 'проверено'],
      ]
  const statuses = isEnglish
    ? ['Manager healthy', 'Client delivery ready', 'No failed nodes']
    : ['Manager исправен', 'Выдача клиентам готова', 'Нет проблемных узлов']

  return (
    <div className="hero-map-preview-wrap">
      <div
        className="hero-map-preview"
        role="img"
        aria-label={isEnglish
          ? 'Illustrative RouteGate product preview centered on a world map of managed VPN nodes'
          : 'Иллюстративный предпросмотр RouteGate с картой управляемых VPN-узлов'}
      >
        <div className="hero-map-preview__chrome">
          <div className="hero-map-preview__brand">
            <img src={assetUrl('routegate-symbol.svg')} alt="" />
            <div><strong>RouteGate</strong><span>{isEnglish ? 'Control plane' : 'Панель управления'}</span></div>
          </div>
          <div className="hero-map-preview__health"><i />{isEnglish ? 'All nodes operational' : 'Все узлы работают'}</div>
        </div>

        <div className="hero-map-preview__metrics">
          {metrics.map(([label, value, hint]) => (
            <article key={label}>
              <span>{label}</span>
              <div><strong>{value}</strong><small>{hint}</small></div>
            </article>
          ))}
        </div>

        <div className="hero-map-preview__map-panel">
          <div className="hero-map-preview__map-heading">
            <div>
              <strong>{isEnglish ? 'Global node topology' : 'Глобальная топология узлов'}</strong>
              <span>{isEnglish ? 'Managed from one control plane' : 'Управление из единой панели'}</span>
            </div>
            <span className="hero-map-preview__online"><i />6 / 6</span>
          </div>

          <div className="hero-map-canvas">
            <img src={assetUrl('world-map-natural-earth.svg')} alt="" />
            <svg className="hero-map-links" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
              {routes.map((path, index) => <path d={path} key={index} />)}
            </svg>
            {nodes.map(node => (
              <span
                className={`hero-map-node${node.hub ? ' is-hub' : ''}`}
                style={{ left: `${node.x}%`, top: `${node.y}%` }}
                key={node.name}
              >
                <i />
                <em>{node.name}</em>
              </span>
            ))}
          </div>

          <div className="hero-map-preview__map-footer">
            <span><i />{isEnglish ? 'Managed node' : 'Управляемый узел'}</span>
            <span><i className="hub" />{isEnglish ? 'Control hub' : 'Центральный узел'}</span>
            <a href="https://www.naturalearthdata.com/" target="_blank" rel="noreferrer">Natural Earth · 1:110m</a>
          </div>
        </div>

        <div className="hero-map-preview__status">
          {statuses.map(status => <span key={status}><i />{status}</span>)}
        </div>
      </div>
      <span className="hero-map-preview__caption">
        {isEnglish ? 'Illustrative product preview · not live telemetry' : 'Иллюстративный предпросмотр · не реальные телеметрические данные'}
      </span>
    </div>
  )
}

function CodePreview({ t }: { t: SiteContent['source'] }) {
  return (
    <div className="code-window">
      <div className="code-toolbar">
        <div><i /><i /><i /></div>
        <a className="code-path" href={sourceCodeUrl} target="_blank" rel="noreferrer">{t.repository}</a>
        <small>Go</small>
      </div>
      <pre aria-label="RouteGate source code preview"><code>
        <span className="code-line"><em>func</em> ValidProfile(profileKey <b>string</b>) <b>bool</b> {'{'}</span>
        <span className="code-line indent"><em>switch</em> strings.TrimSpace(profileKey) {'{'}</span>
        <span className="code-line indent2"><em>case</em> ProfileHostOverview, ProfileVPNCoreStatus,</span>
        <span className="code-line indent2">ProfileManagerCertificate:</span>
        <span className="code-line indent2"><em>return</em> true</span>
        <span className="code-line indent2"><em>default</em>:</span>
        <span className="code-line indent2"><em>return</em> false</span>
        <span className="code-line indent">{'}'}</span>
        <span className="code-line">{'}'}</span>
        <span className="code-line empty"> </span>
        <span className="code-line comment">// Execute runs one compile-time allow-listed diagnostic collector. There is no</span>
        <span className="code-line comment">// command, args, script, or arbitrary shell input in the diagnostic protocol.</span>
        <span className="code-line"><em>func</em> Execute(profileKey <b>string</b>) (map[<b>string</b>]any, <b>error</b>) {'{'}</span>
        <span className="code-line indent"><em>return</em> ExecuteWithOptions(profileKey, Options{'{}'})</span>
        <span className="code-line">{'}'}</span>
      </code></pre>
      <div className="code-status"><span>main</span><span>{t.repository}</span><span>AGPLv3-or-later</span></div>
    </div>
  )
}

function AppHeader({ locale, setLocale, t }: { locale: Locale; setLocale: (value: Locale) => void; t: SiteContent }) {
  const navLinks = (
    <>
      <a href="#product">{t.nav.product}</a>
      <a href="#open-source">{t.nav.openSource}</a>
      <a href={docsUrl} target="_blank" rel="noreferrer">{t.nav.docs}</a>
      <a href="#roadmap">{t.nav.roadmap}</a>
      <a href={releasesUrl} target="_blank" rel="noreferrer">{t.nav.changelog}</a>
    </>
  )

  return (
    <header className="header">
      <div className="container header-inner">
        <a href="#top" aria-label="RouteGate home"><Brand /></a>
        <nav className="desktop-nav" aria-label="Main navigation">{navLinks}</nav>
        <details className="mobile-nav">
          <summary>{locale === 'ru' ? 'Меню' : 'Menu'}</summary>
          <div className="mobile-nav-menu">
            {navLinks}
            <a href="#install">{t.action.start}</a>
          </div>
        </details>
        <div className="header-actions">
          <a className="github-link" href={githubUrl} target="_blank" rel="noreferrer">GitHub <span>↗</span></a>
          <button className="locale" type="button" onClick={() => setLocale(locale === 'ru' ? 'en' : 'ru')} aria-label={locale === 'ru' ? 'Switch to English' : 'Переключить на русский'}>
            <span className={locale === 'en' ? 'is-active' : ''}>EN</span>
            <i>/</i>
            <span className={locale === 'ru' ? 'is-active' : ''}>RU</span>
          </button>
          <a className="button button--small button--primary header-start" href="#install">{t.action.start}</a>
        </div>
      </div>
    </header>
  )
}

export function App({ initialLocale: requestedLocale }: { initialLocale?: Locale } = {}) {
  const initialLocale = useMemo<Locale>(() => {
    if (requestedLocale) return requestedLocale
    const savedLocale = readStoredLocale()
    if (savedLocale) return savedLocale
    return navigator.language.toLowerCase().startsWith('ru') ? 'ru' : 'en'
  }, [requestedLocale])
  const [locale, setLocale] = useState<Locale>(initialLocale)
  const [installCopied, setInstallCopied] = useState(false)
  const [stableRelease, setStableRelease] = useState<StableRelease>({ status: 'loading', tag: null })
  const t = content[locale]
  const installCommand = stableRelease.status === 'ready' && stableRelease.tag
    ? buildInstallCommand(stableRelease.tag)
    : null

  useEffect(() => {
    let unmounted = false
    const controller = new AbortController()
    const timeout = window.setTimeout(() => controller.abort(), 8000)

    const loadPublishedStable = async () => {
      try {
        const response = await fetch(stableReleaseApi, {
          signal: controller.signal,
          headers: { Accept: 'application/vnd.github+json' },
        })
        if (!response.ok) throw new Error('GitHub release lookup failed')
        const payload: unknown = await response.json()
        const tag = publishedStableTag(payload)
        if (!tag) throw new Error('No valid published stable release')
        if (!unmounted) setStableRelease({ status: 'ready', tag })
      } catch {
        // Fail closed: no silently outdated command or mutable branch fallback.
        if (!unmounted) setStableRelease({ status: 'unavailable', tag: null })
      } finally {
        window.clearTimeout(timeout)
      }
    }

    void loadPublishedStable()
    return () => {
      unmounted = true
      controller.abort()
      window.clearTimeout(timeout)
    }
  }, [])

  const handleLocaleChange = (nextLocale: Locale) => {
    setLocale(nextLocale)
    const nextPath = nextLocale === 'ru' ? '/ru/' : '/'
    if (window.location.pathname !== nextPath) {
      window.history.replaceState(null, '', nextPath)
    }
  }

  const handleCopyInstall = async () => {
    if (!installCommand) return
    try {
      await copyText(installCommand)
      setInstallCopied(true)
      window.setTimeout(() => setInstallCopied(false), 1800)
    } catch {
      setInstallCopied(false)
    }
  }

  useEffect(() => {
    const descriptions: Record<Locale, string> = {
      en: 'RouteGate is an open-source self-hosted platform for managed VPN nodes, accounts, devices, routing, and client access.',
      ru: 'RouteGate — открытая платформа для самостоятельного управления Linux VPN-узлами, аккаунтами, устройствами, маршрутизацией и клиентским доступом.',
    }
    document.documentElement.lang = locale
    document.title = locale === 'ru'
      ? 'RouteGate — управление Linux VPN-инфраструктурой'
      : 'RouteGate — Linux VPN infrastructure management'
    document.querySelector<HTMLMetaElement>('meta[name="description"]')?.setAttribute('content', descriptions[locale])
    document.querySelector<HTMLMetaElement>('meta[property="og:title"]')?.setAttribute('content', document.title)
    document.querySelector<HTMLMetaElement>('meta[property="og:description"]')?.setAttribute('content', descriptions[locale])
    document.querySelector<HTMLMetaElement>('meta[property="og:locale"]')?.setAttribute('content', locale === 'ru' ? 'ru_RU' : 'en_US')
    document.querySelector<HTMLMetaElement>('meta[property="og:locale:alternate"]')?.setAttribute('content', locale === 'ru' ? 'en_US' : 'ru_RU')
    const canonicalUrl = locale === 'ru' ? 'https://routegate.org/ru/' : 'https://routegate.org/'
    document.querySelector<HTMLMetaElement>('meta[property="og:url"]')?.setAttribute('content', canonicalUrl)
    document.querySelector<HTMLLinkElement>('link[rel="canonical"]')?.setAttribute('href', canonicalUrl)
    document.querySelector<HTMLMetaElement>('meta[name="twitter:title"]')?.setAttribute('content', document.title)
    document.querySelector<HTMLMetaElement>('meta[name="twitter:description"]')?.setAttribute('content', descriptions[locale])
    persistLocale(locale)
  }, [locale])

  return (
    <div className="site" lang={locale}>
      <a className="skip-link" href="#main-content">{locale === 'ru' ? 'Перейти к содержимому' : 'Skip to content'}</a>
      <AppHeader locale={locale} setLocale={handleLocaleChange} t={t} />
      <main id="main-content">
        <span id="top" className="anchor-target" aria-hidden="true" />
        <section className="hero">
          <div className="hero-grid container">
            <div className="hero-copy">
              <div className="eyebrow"><i />{t.hero.eyebrow}</div>
              <h1>{t.hero.subtitle}</h1>
              <p>{t.hero.description}</p>
              <p className="hero-note">{t.hero.note}</p>
              <div className="hero-actions">
                <a className="button button--primary" href="#install">{t.action.start}<span>→</span></a>
                <a className="button button--ghost" href={githubUrl} target="_blank" rel="noreferrer">
                  <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2a10 10 0 0 0-3.16 19.49c.5.09.68-.22.68-.48v-1.87c-2.78.6-3.37-1.18-3.37-1.18-.45-1.15-1.11-1.46-1.11-1.46-.91-.62.07-.61.07-.61 1 .07 1.53 1.03 1.53 1.03.9 1.53 2.34 1.09 2.91.83.09-.65.35-1.09.64-1.34-2.22-.25-4.55-1.11-4.55-4.94 0-1.09.39-1.98 1.03-2.68-.1-.25-.45-1.27.1-2.64 0 0 .84-.27 2.75 1.02A9.56 9.56 0 0 1 12 6.84a9.5 9.5 0 0 1 2.5.34c1.91-1.3 2.75-1.02 2.75-1.02.55 1.37.2 2.39.1 2.64.64.7 1.03 1.59 1.03 2.68 0 3.84-2.34 4.69-4.57 4.94.36.31.68.92.68 1.86V21c0 .27.18.58.69.48A10 10 0 0 0 12 2Z" /></svg>
                  {t.action.github}
                </a>
              </div>
              <div className="hero-meta">
                <span>Linux</span>
                <span>{locale === 'ru' ? 'Несколько протоколов' : 'Multi-protocol'}</span>
                <span>{locale === 'ru' ? 'Самостоятельное развёртывание' : 'Self-hosted'}</span>
                <span>{locale === 'ru' ? 'Открытый код' : 'Open source'}</span>
              </div>
            </div>
            <HeroMapPreview locale={locale} />
          </div>
        </section>

        <section className="section product-section container" id="product">
          <div className="section-heading"><div><span>{t.product.eyebrow}</span><h2>{t.product.title}</h2></div><p>{t.product.intro}</p></div>
          <div className="feature-grid">
            {t.product.cards.map((card) => (
              <article className="feature-card" key={card.title}>
                <span className="feature-icon"><Icon name={card.icon} /></span>
                <div><h3>{card.title}</h3><p>{card.text}</p></div>
              </article>
            ))}
          </div>
          <a className="section-more" href={docsUrl} target="_blank" rel="noreferrer">
            {locale === 'ru' ? 'Подробнее в документации' : 'Explore documentation'}<span>↗</span>
          </a>
        </section>

        <section className="section workflow-section" id="workflow">
          <div className="container">
            <div className="center-heading"><span>{t.workflow.eyebrow}</span><h2>{t.workflow.title}</h2></div>
            <div className="workflow">
              {t.workflow.steps.map((step, index) => (
                <article key={step.title}>
                  <div className="workflow-icon"><b>{index + 1}</b><Icon name={step.icon} /></div>
                  <div><h3>{step.title}</h3><p>{step.text}</p></div>
                  {index < t.workflow.steps.length - 1 && <i className="workflow-arrow" aria-hidden="true">→</i>}
                </article>
              ))}
            </div>
          </div>
        </section>

        <section className="section source-section" id="open-source">
          <div className="container source-grid">
            <div className="source-copy">
              <span className="section-label">{t.source.eyebrow}</span><h2>{t.source.title}</h2><p>{t.source.text}</p>
              <ul>{t.source.points.map(point => <li key={point}><i>✓</i>{point}</li>)}</ul>
              <a href={githubUrl} target="_blank" rel="noreferrer">{t.action.github}<span>↗</span></a>
            </div>
            <CodePreview t={t.source} />
          </div>
        </section>

        <section className="section deployment-section container" id="install">
          <div className="section-heading"><div><span>{t.deployment.eyebrow}</span><h2>{t.deployment.title}</h2></div><p>{t.deployment.text}</p></div>
          <div className="deployment-grid">
            {t.deployment.cards.map(card => <article key={card.title}><h3>{card.title}</h3><p>{card.text}</p></article>)}
          </div>
          <div className="install-panel">
            <div className="install-panel-heading">
              <div><span>{t.deployment.commandLabel}</span><strong>{
                stableRelease.status === 'ready' && stableRelease.tag
                  ? t.deployment.commandTitle.replace('{version}', stableRelease.tag)
                  : stableRelease.status === 'loading'
                    ? t.deployment.releaseLoadingTitle
                    : t.deployment.releaseUnavailableTitle
              }</strong></div>
              <button type="button" disabled={!installCommand} onClick={handleCopyInstall}>{installCopied ? t.action.copied : t.action.copy}</button>
            </div>
            <pre aria-live="polite"><code>{installCommand ?? (
              stableRelease.status === 'loading'
                ? t.deployment.releaseLoading
                : t.deployment.releaseUnavailable
            )}</code></pre>
            <div className="install-panel-footer">
              <span>{stableRelease.status === 'ready'
                ? stableRelease.tag === 'v0.1.0'
                  ? t.deployment.legacyReleaseNote
                  : t.deployment.commandNote
                : t.deployment.releaseUnavailableNote}</span>
              <a href={releasesUrl} target="_blank" rel="noreferrer">{t.deployment.releaseListLabel}<span>↗</span></a>
              <a href={installGuideUrl} target="_blank" rel="noreferrer">{t.action.installGuide}<span>↗</span></a>
            </div>
          </div>
        </section>

        <section className="section roadmap-section" id="roadmap">
          <div className="container">
            <div className="section-heading"><div><span>{t.roadmap.eyebrow}</span><h2>{t.roadmap.title}</h2></div><p>{t.roadmap.intro}</p></div>
            <div className="roadmap-grid">
              {t.roadmap.columns.map(column => (
                <article key={column.title}>
                  <h3>{column.title}</h3>
                  <ul>{column.items.map(item => <li key={item}>{item}</li>)}</ul>
                </article>
              ))}
            </div>
            <a className="section-more" href={docsUrl} target="_blank" rel="noreferrer">
              {locale === 'ru' ? 'Подробнее в документации' : 'Explore documentation'}<span>↗</span>
            </a>
          </div>
        </section>

        <section className="section faq-section container" id="faq">
          <div className="section-heading"><div><span>{t.faq.eyebrow}</span><h2>{t.faq.title}</h2></div><p>{t.faq.intro}</p></div>
          <div className="faq-grid">
            {t.faq.items.map(item => (
              <details key={item.question}>
                <summary>{item.question}</summary>
                <p>{item.answer}</p>
              </details>
            ))}
          </div>
        </section>

        <section className="final-cta container" id="start">
          <div className="cta-mark"><img src={assetUrl('routegate-symbol.svg')} alt="" /></div>
          <div><h2>{t.cta.title}</h2><p>{t.cta.text}</p></div>
          <div><a className="button button--light" href={installGuideUrl} target="_blank" rel="noreferrer">{t.action.installGuide}<span>→</span></a><a className="button button--outline" href={githubUrl} target="_blank" rel="noreferrer">{t.action.github}</a></div>
        </section>
      </main>

      <footer className="footer">
        <div className="container footer-grid">
          <div><Brand /><p>{t.footer.description}</p><small>© 2026 RouteGate</small></div>
          <div><strong>{t.footer.project}</strong><a href="#product">{t.footer.items[0]}</a><a href="#roadmap">{t.footer.items[1]}</a></div>
          <div><strong>{t.footer.resources}</strong><a href={docsUrl} target="_blank" rel="noreferrer">{t.footer.items[2]}</a><a href={githubUrl} target="_blank" rel="noreferrer">{t.footer.items[3]}</a></div>
          <div><strong>{t.footer.legal}</strong><a href={releasesUrl} target="_blank" rel="noreferrer">{t.footer.items[4]}</a><a href={securityUrl} target="_blank" rel="noreferrer">{t.footer.items[5]}</a><a href={licenseUrl} target="_blank" rel="noreferrer">{t.footer.items[6]}</a></div>
        </div>
      </footer>
    </div>
  )
}

