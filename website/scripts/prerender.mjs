import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const dist = join(here, '..', 'dist')
const source = await readFile(join(dist, 'index.html'), 'utf8')

const pages = {
  en: {
    lang: 'en',
    locale: 'en_US',
    alternateLocale: 'ru_RU',
    url: 'https://routegate.org/',
    title: 'RouteGate — Linux VPN infrastructure management',
    description: 'RouteGate is an open-source self-hosted platform for managed VPN nodes, accounts, devices, routing, and client access.',
    switchHref: '/ru/',
    switchLabel: 'Русский',
    hero: 'Your Linux VPN infrastructure. Under your control.',
    intro: 'Open-source self-hosted control plane for VPN nodes, accounts, devices, routing, user access, and operational lifecycle.',
    productTitle: 'From VPN nodes to user access',
    productItems: [
      'Managed local and remote Linux VPN nodes through RouteGate Agent.',
      'The original public MVP v0.1.0 supported VLESS / Reality; newer protocols are available only when included in a published release.',
      'Access & Devices, subscriptions, QR delivery, revocation, and rotation.',
      'Routing profiles delivered according to each VPN client’s capabilities.',
    ],
    installTitle: 'Deploy on a clean Ubuntu VPS',
    installText: 'The public clean-host path targets Ubuntu 24.04 LTS on amd64 with PostgreSQL, systemd, nginx, HTTPS, and a single-use administrator setup link.',
    roadmapTitle: 'Roadmap',
    roadmapItems: [
      'Original public MVP v0.1.0: initial VLESS / Reality clean-host path on Ubuntu 24.04 LTS amd64.',
      'Current main: multi-protocol management, Access & Devices, User Portal, routing profiles, and verified updates.',
      'Next: broader validation and safer automation without hidden account moves.',
    ],
    securityTitle: 'Open source and security',
    securityText: 'RouteGate is AGPLv3-or-later. Security reports should use the private disclosure process described in SECURITY.md.',
  },
  ru: {
    lang: 'ru',
    locale: 'ru_RU',
    alternateLocale: 'en_US',
    url: 'https://routegate.org/ru/',
    title: 'RouteGate — управление Linux VPN-инфраструктурой',
    description: 'RouteGate — открытая платформа для самостоятельного управления Linux VPN-узлами, аккаунтами, устройствами, маршрутизацией и клиентским доступом.',
    switchHref: '/',
    switchLabel: 'English',
    hero: 'Linux VPN-инфраструктура под вашим контролем',
    intro: 'Открытая платформа для самостоятельного управления VPN-узлами, аккаунтами, устройствами, маршрутизацией, пользовательским доступом и эксплуатационным жизненным циклом.',
    productTitle: 'От VPN-узла до пользовательского доступа',
    productItems: [
      'Управляемые локальные и удалённые Linux VPN-узлы через RouteGate Agent.',
      'Первый публичный MVP v0.1.0 поддерживал VLESS / Reality; дополнительные протоколы доступны только в тех выпусках, где они опубликованы.',
      'Доступ и устройства, подписки, QR-коды, отзыв и ротация доступа.',
      'Профили маршрутизации с доставкой с учётом возможностей конкретного VPN-клиента.',
    ],
    installTitle: 'Развёртывание на чистом Ubuntu VPS',
    installText: 'Публичный путь установки рассчитан на Ubuntu 24.04 LTS amd64 с PostgreSQL, systemd, nginx, HTTPS и одноразовой ссылкой для создания администратора.',
    roadmapTitle: 'Дорожная карта',
    roadmapItems: [
      'Первый публичный MVP v0.1.0: исходная установка VLESS / Reality на Ubuntu 24.04 LTS amd64.',
      'Текущая ветка main: несколько протоколов, Доступ и устройства, Портал пользователя, маршрутизация и проверяемые обновления.',
      'Дальше: более широкая проверка и безопасная автоматизация без скрытых переносов аккаунтов.',
    ],
    securityTitle: 'Открытый код и безопасность',
    securityText: 'RouteGate распространяется по AGPLv3-or-later. Сообщения об уязвимостях следует отправлять через приватный процесс, описанный в SECURITY.md.',
  },
}

function esc(value) {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
}

function list(items) {
  return items.map((item) => '<li>' + esc(item) + '</li>').join('')
}

function fallback(page) {
  return [
    '<main class="seo-prerender">',
    '<header>',
    '<a class="seo-brand" href="/">RouteGate</a>',
    '<nav aria-label="Language and project links">',
    '<a href="' + page.switchHref + '">' + esc(page.switchLabel) + '</a>',
    '<a href="https://github.com/routegate/RouteGate">GitHub</a>',
    '<a href="https://github.com/routegate/RouteGate/tree/main/docs">Docs</a>',
    '</nav>',
    '</header>',
    '<h1>' + esc(page.hero) + '</h1>',
    '<p>' + esc(page.intro) + '</p>',
    '<section><h2>' + esc(page.productTitle) + '</h2><ul>' + list(page.productItems) + '</ul></section>',
    '<section><h2>' + esc(page.installTitle) + '</h2><p>' + esc(page.installText) + '</p></section>',
    '<section><h2>' + esc(page.roadmapTitle) + '</h2><ul>' + list(page.roadmapItems) + '</ul></section>',
    '<section><h2>' + esc(page.securityTitle) + '</h2><p>' + esc(page.securityText) + '</p></section>',
    '</main>',
  ].join('')
}

function replaceMeta(html, attribute, key, value) {
  const pattern = new RegExp('<meta\\s+' + attribute + '="' + key + '"[^>]*>', 'i')
  return html.replace(pattern, '<meta ' + attribute + '="' + key + '" content="' + esc(value) + '">')
}

function render(page) {
  let html = source
  html = html.replace(/<html\s+lang=["'][^"']*["']>/i, '<html lang="' + page.lang + '">')
  html = html.replace(/<title>[^<]*<\/title>/i, '<title>' + esc(page.title) + '</title>')
  html = replaceMeta(html, 'name', 'description', page.description)
  html = replaceMeta(html, 'property', 'og:title', page.title)
  html = replaceMeta(html, 'property', 'og:description', page.description)
  html = replaceMeta(html, 'property', 'og:url', page.url)
  html = replaceMeta(html, 'property', 'og:locale', page.locale)
  html = replaceMeta(html, 'property', 'og:locale:alternate', page.alternateLocale)
  html = replaceMeta(html, 'name', 'twitter:title', page.title)
  html = replaceMeta(html, 'name', 'twitter:description', page.description)
  html = html.replace(/<link\s+rel="canonical"[^>]*>/i, '<link rel="canonical" href="' + page.url + '">')
  html = html.replace(/<div\s+id="root"><\/div>/i, '<div id="root">' + fallback(page) + '</div>')
  return html
}

const englishHtml = render(pages.en)
const russianHtml = render(pages.ru)

function assertGenerated(label, html, expected) {
  for (const value of expected) {
    if (!html.includes(value)) {
      throw new Error(label + ' prerender is missing: ' + value)
    }
  }
}

assertGenerated('English', englishHtml, [
  'class="seo-prerender"',
  '<html lang="en">',
  '<link rel="canonical" href="https://routegate.org/">',
  'Your Linux VPN infrastructure. Under your control.',
])

assertGenerated('Russian', russianHtml, [
  'class="seo-prerender"',
  '<html lang="ru">',
  '<link rel="canonical" href="https://routegate.org/ru/">',
  'Linux VPN-инфраструктура под вашим контролем',
])

await writeFile(join(dist, 'index.html'), englishHtml)
await mkdir(join(dist, 'ru'), { recursive: true })
await writeFile(join(dist, 'ru', 'index.html'), russianHtml)
