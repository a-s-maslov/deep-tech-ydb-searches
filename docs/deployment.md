# Развёртывание стенда

Это инструкция для первого запуска из чистых клонов. После её выполнения
оператор работает только по [demo.md](demo.md).

## 1. Предварительные требования

На управляющей Linux-машине нужны:

- Python 3.11+ и пакет `python3-venv`;
- Go 1.24;
- Docker для отдельной Grafana и VictoriaMetrics;
- YDB CLI с рабочим профилем и способом получать свежий токен;
- SSH-доступ с agent forwarding к узлам YDB;
- около 60 ГБ свободного места на время первой распаковки данных.

Кластер YDB должен поддерживать `fulltext_relevance`,
`vector_kmeans_tree`, `HybridRank` и запись в таблицу с обоими индексами.
Для DML-потока должен быть включён `EnableIndexStreamWrite`. Не полагайтесь
только на номер версии: `bootstrap` и `preflight` выполняют фактические
capability- и query-проверки.

Для короткого этапа добавления dynamic-узлов на выделенном demo-стенде
проверена настройка Hive:

```yaml
hive_config:
  tablet_kick_cooldown_period: 30
```

Она ускоряет повторное перемещение таблеток после изменения числа узлов и не
задаёт размещение вручную. Actor system остаётся в штатном auto-config.

## 2. Клоны и локальные файлы

Клонировать оба репозитория в один каталог:

```bash
mkdir -p ~/ydb-work/deep-tech-night
cd ~/ydb-work/deep-tech-night
git clone https://github.com/a-s-maslov/deep-tech-ydb-searches.git
git clone https://github.com/a-s-maslov/chaos-md.git
```

Все команды проекта далее выполняются из его корня:

```bash
cd ~/ydb-work/deep-tech-night/deep-tech-ydb-searches
```

Создать локальный workload-конфиг:

```bash
cp config/workload.stand.example.json config/workload.stand.json
```

В нём обязательно проверить:

- `connection_string` и способ аутентификации;
- `table_path` — команда удаления действует только на эту таблицу;
- пути `document_file` и `query_file` для нужного профиля;
- адреса `metrics` и `observer`, свободные на управляющей машине.

Файлы `config/workload.stand.json`, `.runtime/` и `data/` исключены из Git.
Не добавляйте токен YDB в JSON: wrapper получает его перед каждым запуском.

## 3. Настройка chaos-md

Создать конфигурацию стенда и локальные секреты по примерам `chaos-md`:

```bash
cd ../chaos-md
cp env.example.sh env-stand.sh
./switch-config.sh stand
cp workload/manager.env.example.sh workload/env.local.sh
```

В `env-stand.sh` задать как минимум:

- `MON_HOST`, `CLUSTER_HOSTS`, `SINGLE_HOST`;
- `DC_HOSTS` — узлы одного отказного домена; для проверенного стенда это
  `ydb-s7.example.com`, `ydb-s8.example.com`, `ydb-s9.example.com`;
- `DYNAMIC_NODE_HOSTS` и имя только dynamic/tenant systemd service;
- YDB monitoring ports и SSH-параметры;
- отдельные каталоги данных VictoriaMetrics и Grafana.

В `workload/env.local.sh` задать интеграцию с проектом:

```bash
CHAOS_WORKLOAD_TYPE="search"
SEARCH_WORKLOAD_MODE="binary"
SEARCH_WORKLOAD_BIN="$HOME/ydb-work/deep-tech-night/deep-tech-ydb-searches/scripts/stand/run-workload-with-token.sh"
SEARCH_WORKLOAD_REAL_BIN="$HOME/ydb-work/deep-tech-night/deep-tech-ydb-searches/bin/search-workload"
SEARCH_WORKLOAD_CONFIG="$HOME/ydb-work/deep-tech-night/deep-tech-ydb-searches/config/workload.stand.json"
SEARCH_WORKLOAD_METRICS_URL="http://127.0.0.1:19091/metrics"
YDB_CLI="$HOME/ydb/bin/ydb"
YDB_PROFILE="db1"
```

`run-workload-with-token.sh` вызывает YDB CLI, получает свежий токен и передаёт
его только окружению Go-процесса. Токен не записывается в репозиторий.

Системные хаос-параметры и пароль Grafana хранить в корневом
`chaos-md/env.local.sh`, который также игнорируется Git. Подробные значения и
требования каждого немезиса описаны в README самого `chaos-md`.

## 4. Мониторинг

Установить отдельный стек из каталога `chaos-md`; существующие Grafana и
Prometheus он не переиспользует:

```bash
cd ../chaos-md
./grafana/01-victoria.sh
./grafana/02-node-exporter.sh
./grafana/03-grafana.sh
./grafana/04-dashboards-provision.sh
```

Скрипты поддерживают `--check` и `--dry-run`. При конфликте имён существующих
контейнеров они завершаются с ошибкой; замена требует явного `--replace`.

В `env-stand.sh` цели поискового стенда должны соответствовать workload-
конфигу:

```bash
WORKLOAD_METRICS_TARGET="127.0.0.1:19091"
OBSERVER_METRICS_TARGET="127.0.0.1:19092"
```

Создать локальный service-account token только для Grafana-аннотаций:

```bash
./grafana/configure-annotations.sh --env-file ./env.local.sh --ttl 30d
./grafana/configure-annotations.sh --env-file ./env.local.sh --check
python3 grafana/tests/dashboard-smoke.py
```

Observer работает независимо от workload и должен оставаться запущенным между
профилями. Он читает `.sys/partition_stats`, а также публикует последний
сохранённый offline-отчёт качества:

```bash
cd ../deep-tech-ydb-searches
bash scripts/stand/manage-observer.sh restart
bash scripts/stand/manage-observer.sh status
```

## 5. Подготовка данных и стенда

Штатный путь — одна команда:

```bash
cd ~/ydb-work/deep-tech-night/deep-tech-ydb-searches
./scripts/demo.sh bootstrap --yes
```

Она:

1. собирает Go workload;
2. скачивает и проверяет зафиксированные источники;
3. строит `scale-1m`, если его ещё нет;
4. удаляет и пересоздаёт только настроенную workshop-таблицу;
5. загружает миллион документов и создаёт оба индекса;
6. прогревает и стабилизирует партиции на девяти dynamic-узлах;
7. возвращает три узла и исходное состояние demo;
8. генерирует и исполняет браузерные YQL;
9. проверяет workload, observer и chaos-управление.

Команда требует живую SSH-сессию с agent forwarding. Не запускайте её через
`nohup`: она управляет systemd services на узлах и должна немедленно сообщить
об ошибке доступа.

До подтверждения можно увидеть план без изменений:

```bash
./scripts/demo.sh --dry-run bootstrap --yes
```

Для отдельной проверки готовых артефактов:

```bash
bash scripts/dataset.sh status scale-1m
```

## 6. Приёмка

После `bootstrap` выполнить:

```bash
./scripts/demo.sh preflight
./scripts/demo.sh status
```

Ожидаем:

- профиль `scale-1m`, 1 000 000 исходных документов;
- `ft_idx` и `vec_idx` в состоянии `READY`;
- три активных dynamic-узла;
- одна фиксированная рабочая партиция полнотекстового индекса в первом кадре;
- workload остановлен, observer отвечает;
- все три browser YQL возвращают результаты;
- команды `chaos-md` доступны по SSH.

Проверить запуск небольшого фона и сразу вернуть исходное состояние:

```bash
./scripts/demo.sh stage overview
./scripts/demo.sh status
./scripts/demo.sh stop
./scripts/demo.sh prepare
```

После этого стенд готов к [операторскому сценарию](demo.md).

## 7. Обновление уже подготовленного стенда

Если изменился только код или документация, данные повторно загружать не надо:

```bash
git pull --ff-only
bash scripts/stand/build-workload.sh
./scripts/demo.sh prepare
./scripts/demo.sh preflight
```

Если изменились `config/sources.json`, схема, формат runtime-артефактов или
параметры создания индекса, выполнить полный `bootstrap --yes`.

## Восстановление

Единая безопасная команда снимает активные отказы, останавливает workload и
возвращает сервисы:

```bash
./scripts/demo.sh recover
./scripts/demo.sh prepare
```

Если шаг завершился ошибкой, сначала запускать `status`, а не повторять
низкоуровневые команды:

```bash
./scripts/demo.sh status
```

Контроллер сериализует операции через `.runtime/demo.lock`; сообщение
`another demo command is running` означает, что предыдущая команда ещё жива.
Убивать её или удалять lock-файл следует только после проверки процесса.
