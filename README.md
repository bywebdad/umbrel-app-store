# WEBDAD — Community App Store для Umbrel

Приложения WEBDAD для umbrelOS. Добавить стор: App Store → `⋯` → Community App Stores → `https://github.com/bywebdad/umbrel-app-store`.

| Приложение | Папка | Образ |
|---|---|---|
| NordVPN (VPN + Meshnet) | `webdad-nordvpn/` | `ghcr.io/bywebdad/umbrel-nordvpn` |

---

## NordVPN для Umbrel (VPN + Meshnet)

Официальный Linux-клиент NordVPN, который

- пускает **весь исходящий трафик Umbrel и всех приложений** через NordVPN;
- включает **Meshnet**, чтобы телефон и ноутбук с обычным приложением NordVPN видели Umbrel и OpenClaw откуда угодно, без второго VPN-приложения;
- управляется через веб-панель за логином Umbrel: вход по токену, подключение, Kill Switch, автоподключение, Meshnet и права пиров, allowlist.

> Пакет неофициальный, с Nord Security не связан. Внутри только официальный клиент из `repo.nordvpn.com`.

## Как это устроено

```
umbrelOS (хост)
├── daemon  ── nordvpnd, network_mode: host, privileged
│              VPN-маршруты и интерфейс Meshnet действуют на весь хост
│              /var/lib/nordvpn → ${APP_DATA_DIR}/data/nordvpn  (логин и Meshnet-устройство переживают перезапуск)
│              /run/nordvpn     → общий volume с сокетом демона
├── web     ── панель (Go, ~10 МБ), за app_proxy → логин Umbrel
│              команды выполняет официальный CLI `nordvpn` через сокет
└── app_proxy (Umbrel) → порт 8794
```

Ничего не ставится на сам хост umbrelOS, поэтому OTA-обновления и перезагрузки приложение не ломают. Удалил приложение — следов на хосте не осталось.

## Установка

1. Push в `main` запускает workflow **image** во вкладке Actions: тесты и сборка образа `ghcr.io/bywebdad/umbrel-nordvpn` для amd64 и arm64. Тег образа берётся из `version` в `webdad-nordvpn/umbrel-app.yml`, git-теги не нужны.
2. **Пакет в GHCR должен быть публичным** (делается один раз): github.com/bywebdad → Packages → `umbrel-nordvpn` → Package settings → Change visibility → Public. Без этого Umbrel не скачает образ.
3. В Umbrel: App Store → `⋯` → **Community App Stores** → вставь `https://github.com/bywebdad/umbrel-app-store` → открой магазин **WEBDAD** → установи **NordVPN**.

## Первый запуск

1. Получи токен: [Nord Account](https://my.nordaccount.com/dashboard/nordvpn/manual-configuration/) → NordVPN → Manual setup → **Access token**.
2. Открой приложение NordVPN на Umbrel → вставь токен → «Войти».
3. Включи **Meshnet**. В списке появится имя Umbrel вида `xxx.nord`.
4. Подключи VPN: пусто → быстрейший сервер, или `Poland`, `Germany`, `P2P` и т. п.
5. Включи **Автоподключение**: после перезагрузки Umbrel VPN поднимется сам. Локация берётся из поля над кнопкой «Подключить».

### Проверка

- Umbrel → Settings → Advanced settings → **Terminal → umbrelOS**:
  ```bash
  curl -s https://ipinfo.io     # должен показать IP и страну сервера NordVPN
  ip route get 1.1.1.1           # маршрут через интерфейс nordlynx
  ```
- С телефона (NordVPN включён, Meshnet включён): открой `http://<имя-umbrel>.nord` — дашборд Umbrel; OpenClaw — `http://<имя-umbrel>.nord:<порт-OpenClaw>`.
- Если телефон не видит Umbrel: в панели → Meshnet → введи имя телефона → «входящие» → «Разрешить».

## Важные оговорки

- **Не проверено на живом Umbrel.** Панель покрыта тестами и проверена с имитацией CLI, YAML и скрипты прошли валидацию. Но сам образ с `nordvpnd` в этой среде собрать и запустить нельзя (нет доступа к `repo.nordvpn.com` и Docker). Первый запуск делай, когда есть физический или LAN-доступ к Umbrel.
- **Если что-то пошло не так с сетью** — удали приложение и перезагрузи Umbrel. umbrelOS при перезагрузке сбрасывает изменения хоста, включая правила firewall и маршруты, которые мог оставить упавший демон.
- **DNS-запросы хоста идут мимо туннеля.** NordVPN меняет DNS только внутри своего контейнера. Docker и umbrelOS продолжают спрашивать DNS у роутера: сам трафик зашифрован, но провайдер видит, какие домены резолвит сервер. Если это важно, поставь в роутере DNS NordVPN (`103.86.96.100`, `103.86.99.100`) или другой DoH/DoT-резолвер.
- **Kill Switch** по умолчанию выключен. Если включить и VPN упадёт, у Umbrel не будет интернета (включая обновления и LLM API для OpenClaw), пока соединение не восстановится. LAN и Meshnet при этом работают.
- **LAN Discovery** держи включённым: он оставляет вне туннеля локальную сеть и сети Docker (10.x, 172.16–31.x, 192.168.x). Без него Umbrel может пропасть из локальной сети.
- **Tailscale.** Если оставляешь его установленным параллельно, добавь `100.64.0.0/10` в allowlist (в панели или `NORD_ALLOWLIST_SUBNETS` в `docker-compose.yml`). Но два VPN, которые оба управляют маршрутами хоста, — это лишний источник проблем. Цель этого приложения — заменить Tailscale.
- **Входящие соединения из интернета** (проброс портов на роутере) при включённом VPN работать не будут: ответы уйдут через туннель. Для удалённого доступа используй Meshnet.

## Обновление

- Новая версия: подними `version` в `webdad-nordvpn/umbrel-app.yml` и тег образа в `docker-compose.yml` (одинаковые) → push в `main`. CI соберёт образ с этим тегом, Umbrel предложит обновление. Свежий клиент NordVPN подтягивается при каждой сборке; закрепить версию можно через Actions → image → Run workflow.
- Логин и Meshnet-устройство хранятся в данных приложения и переживают обновления. При переустановке приложения данные удаляются: нужно войти заново, и Umbrel появится в Meshnet как новое устройство.

## Разработка

```bash
cd image/panel && go test ./...            # тесты панели
go run . -listen 127.0.0.1:8080 -nordvpn ./testdata/fake-nordvpn   # панель против фейкового CLI
```

Структура:

```
umbrel-app-store.yml          id магазина: webdad
webdad-nordvpn/               приложение Umbrel (manifest, compose, иконка)
image/Dockerfile              образ: Go-панель + официальный nordvpn
image/rootfs/usr/local/bin/   entrypoint (daemon|panel) и bootstrap базовых настроек
image/panel/                  веб-панель на Go без внешних зависимостей
.github/workflows/image.yml   тесты + multi-arch сборка в GHCR
```
