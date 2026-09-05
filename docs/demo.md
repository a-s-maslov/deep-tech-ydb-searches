# Демонстрация: поиск, эластичность и отказы

Все операторские команды запускаются из корня проекта:

```bash
cd /home/bastion/ydb-work/deep-tech-night/deep-tech-ydb-searches
```

Контроллер `scripts/demo.sh` выполняет низкоуровневые команды `chaos-md`,
показывает каждый шаг и проверяет результат. Повторять вручную внутренние
`stop/configure/start/status` не нужно.

Ориентир для 30-минутного показа: обзор — 8 минут, full-text split — 5 минут,
масштабирование dynamic-узлов — 8 минут, четыре отказа — 8 минут, резерв —
3 минуты. Между разделами workload не останавливается без необходимости:
команда следующего этапа сама безопасно меняет профиль.

## 1. Полное воспроизведение с пустой базы

Этот шаг выполняется заранее, не во время выступления:

```bash
# Пересоздать только workshop-таблицу, загрузить миллион документов,
# построить индексы, прогреть топологию и подготовить начало демонстрации.
./scripts/demo.sh bootstrap --yes
```

Запускать команду в обычной SSH-сессии с включённым agent forwarding и не
отсоединять её через `nohup`: контроллер управляет YDB-узлами по SSH и заранее
завершается с ошибкой, если доступ к ним потерян.

Команда:

1. собирает текущий Go workload;
2. скачивает или проверяет профиль `scale-1m`;
3. пересоздаёт только таблицу из `config/workload.stand.json`;
4. загружает документы и строит оба индекса;
5. на девяти dynamic-узлах запускает прогрев `elasticity-demand`;
6. ждёт не меньше пяти минут, затем три последовательных интервала проверяет
   стабильность партиций, достижение не менее 95% target и отсутствие ошибок,
   ретраев и пропущенных запусков;
7. останавливает прогрев и закрепляет достигнутые минимумы;
8. возвращает три dynamic-узла;
9. пересоздаёт полнотекстовый индекс с одной фиксированной партицией;
10. генерирует YQL для браузера, исполняет именно эти inline-запросы через
    Table API и проверяет observer.

`bootstrap` требует `--yes`, потому что удаляет и пересоздаёт настроенную
workshop-таблицу. Другие пользовательские таблицы он не затрагивает. Загрузка
исходного корпуса может быть долгой; повторный запуск использует локальный кэш.

Ожидаем в конце: три dynamic-узла, миллион исходных документов, оба индекса
`READY`, одна рабочая партиция полнотекстового индекса, workload остановлен.

## 2. Подготовка перед каждым показом

```bash
# Вернуть уже загруженный стенд к исходному состоянию демонстрации.
./scripts/demo.sh prepare
```

Команда останавливает старую нагрузку, очищает только зарезервированный
DML-диапазон, возвращает три узла, пересоздаёт только полнотекстовый индекс,
фиксирует его на одной партиции, проверяет поиск, обновляет токен observer и
генерирует браузерные YQL-файлы.

Если между подготовкой и выступлением прошло много времени:

```bash
# Ничего не менять, только проверить данные, YDB, observer и права chaos.
./scripts/demo.sh preflight
```

Единая диагностика текущего состояния:

```bash
./scripts/demo.sh status
```

## 3. Обзор: YDB UI и Grafana

Перед открытием браузера:

```bash
# Вернуть три узла и запустить фон: fulltext/vector/hybrid = 4/2/1 RPS и 7 DML/с.
./scripts/demo.sh stage overview
```

Оставить workload работающим при переходе между YDB UI и Grafana.
Пока выполняются запросы в UI, на дашборде успевает накопиться несколько минут
фоновой истории; отдельная пауза перед Grafana не нужна.

### YDB UI

1. Войти под заранее подготовленной demo-учётной записью YDB. Пароль хранится
   только в локальной памятке стенда и не публикуется в Git.
2. Открыть базу, затем `Tables` → `deep_tech_search_documents`.
3. В `Columns` показать `id`, `docid`, `title`, `text`, `embedding`.
4. В `Indexes` показать `ft_idx` и `vec_idx`; оба должны быть `READY`.
5. Открыть `Query` и последовательно копировать блоки ниже. Они полностью
   готовы к выполнению; открывать отдельные файлы во время записи не нужно.

#### Полнотекстовый индекс

Показать DDL, но не выполнять: индекс уже создан. Сказать: «Индекс строится по
единой колонке `text`, а `title` и `docid` включены в покрытие. Standard
tokenizer дополнен lowercase и русским Snowball».

```yql
ALTER TABLE `deep_tech_search_documents`
ADD INDEX `ft_idx` GLOBAL USING fulltext_relevance
ON (text) COVER (title, docid)
WITH (
    tokenizer = standard,
    use_filter_lowercase = true,
    use_filter_snowball = true,
    language = "russian"
);
```

#### Полнотекстовый запрос

Выполнить. Сказать: «Это короткий keyword-запрос из того же набора, что
получает full-text поток workload. Оба информативных терма обязательны; BM25
ранжирует top-10».

```yql
-- Вопрос: «Когда начался Кари́бский кризис?»
$query = "Карибский кризис"u;

-- relevance повторяется в WHERE: alias из SELECT там ещё недоступен.
SELECT
    docid,
    title,
    FullTextScore(
        text,
        $query,
        "Or" AS DefaultOperator,
        "100%" AS MinimumShouldMatch,
        1.0 AS K1,
        0.25 AS B
    ) AS relevance
FROM `deep_tech_search_documents` VIEW `ft_idx`
WHERE FullTextScore(
    text,
    $query,
    "Or" AS DefaultOperator,
    "100%" AS MinimumShouldMatch,
    1.0 AS K1,
    0.25 AS B
) > 0
ORDER BY
    relevance DESC,
    docid ASC
LIMIT 10;
```

#### Векторный индекс

Показать DDL, но не выполнять. Сказать: «Синхронный k-means tree хранит
768-мерные mDPR-вектора и использует ту же inner-product метрику, что исходный
набор».

```yql
ALTER TABLE `deep_tech_search_documents`
ADD INDEX `vec_idx` GLOBAL SYNC USING vector_kmeans_tree
ON (embedding) COVER (title, docid)
WITH (
    similarity = "inner_product",
    vector_type = "float",
    vector_dimension = 768,
    levels = 2,
    clusters = 128,
    overlap_clusters = 3
);
```

#### Векторный запрос

Выполнить. Сказать: «В комментарии виден исходный естественный вопрос.
`TopSize=24` задаёт ширину ANN-поиска, а `$target` содержит реальный
768-мерный embedding этого вопроса».

```yql
PRAGMA ydb.KMeansTreeSearchTopSize = "24";

-- Вопрос: «Когда начался Кари́бский кризис?»

$target = Unwrap(String::Base64Decode("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"));
SELECT
    docid,
    title
FROM `deep_tech_search_documents` VIEW `vec_idx`
ORDER BY Knn::InnerProductSimilarity(
    embedding,
    $target
) DESC
LIMIT 10;
```

#### Гибридный запрос

Выполнить. Сказать: «Тот же естественный вопрос представлен и лексическим
текстом, и embedding. `HybridRank` нормализует две оценки и объединяет
результаты штатных индексов».

```yql
PRAGMA ydb.KMeansTreeSearchTopSize = "24";

-- Вопрос: «Когда начался Кари́бский кризис?»
$query = "начался Карибский кризис"u;

$target = Unwrap(String::Base64Decode("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"));

SELECT
    docid,
    title
FROM `deep_tech_search_documents`
ORDER BY HybridRank(
    FullTextScore(
        text,
        $query,
        "Or" AS DefaultOperator,
        "50%" AS MinimumShouldMatch,
        1.0 AS K1,
        0.25 AS B
    ),
    Knn::InnerProductSimilarity(
        embedding,
        $target
    ),
    "linear" AS Mode,
    (0.075, 1.0) AS Weights,
    true AS Normalize,
    ("ft_idx", "vec_idx") AS Indexes,
    (10, 10) AS Limits
)
LIMIT 10;
```

Блоки отформатированы для чтения с экрана, но используют те же builders,
значения и операции, что workload. Драйвер связывает `$query` и `$target`.
В workload `$target` передаётся двоичным параметром. В ручных UI-запросах
тот же вектор декодируется из одной Base64-строки. Её проговаривать и
прокручивать не нужно.

Повторно создать компактные runtime-файлы можно командой:

```bash
./scripts/demo.sh scripts
```

Ожидаем: все три SELECT возвращают до десяти `docid` без ошибок. `prepare` и
`preflight` заранее исполняют их с теми же литералами через Table API;
варианты в инструкции отличаются только пробелами, переносами и комментариями.

### Grafana

Открыть `Deep Tech: YDB Search Demo`, выбрать `Last 15 minutes`, refresh
`5s`. Если сессия входа истекла: пользователь `admin`, пароль взять из
локального `../chaos-md/env.local.sh` (`GRAFANA_ADMIN_PASSWORD`). Секрет в
публичный репозиторий не записывается.

Первый ряд:

- `Поисковые запросы и DML, RPS` — фактический и целевой поток;
- `Задержка p95, мс` — задержка fulltext, vector, hybrid и DML;
- `Ошибки и ретраи, RPS` — ошибки YDB и повторные попытки клиента.

Второй ряд:

- `User pool CPU по dynamic-узлам, %`;
- `Самая загруженная таблетка, %`;
- `Партиции` основной таблицы и двух индексов.

Сказать:

> Верхний ряд показывает результат со стороны приложения. Нижний связывает его
> с вычислительными ресурсами и таблетками YDB. Профили нагрузки, управляющие
> действия и отказы отмечаются теми же переключаемыми событиями.

## 4. Эластичность полнотекстового индекса

Исходное состояние: работает `overview`, полнотекстовый индекс имеет одну
партицию.

```bash
# Сменить фон на усиленный fulltext-профиль, сохранив одну партицию.
./scripts/demo.sh stage fulltext-limit
```

Ожидаем: одна таблетка полнотекстового индекса приближается к пределу,
фактический fulltext RPS выходит на плато ниже target, задержка растёт.
Обычно достаточно 30–60 секунд; переходить дальше после появления устойчивого
плато, а не по первому десятисекундному интервалу.

Сказать:

> Мы увеличили полнотекстовую ветку, но оставили её внутреннюю таблицу на одной
> партиции. Теперь ограничение видно одновременно по RPS, задержке и загрузке
> самой горячей таблетки.

```bash
# Включить автосплит, не останавливая и не перезапуская workload.
./scripts/demo.sh action fulltext-split
```

Ожидаем в течение переходного интервала: партиций становится больше одной,
загрузка горячей таблетки снижается, фактический поток достигает target.
Короткий пик задержки или ретраев во время split допустим.

Сказать:

> Клиентский поток не менялся. YDB разделила внутреннюю таблицу индекса,
> распределила работу между таблетками и сняла прежнее ограничение.

## 5. Эластичность dynamic-узлов

```bash
# На трёх узлах запустить устойчивую базу 120/60/30 = 210 поисковых RPS.
./scripts/demo.sh stage capacity-base
```

Не использовать первые интервалы после перехода. Ожидаем после прогрева:
три линии CPU, около 210 поисковых RPS, без постоянных ошибок и ретраев.
Критерий — три последовательных десятисекундных интервала у target; на
проверенном стенде это занимает около минуты.

```bash
# На тех же трёх узлах увеличить тот же микс до 288/144/72 = 504 поисковых RPS.
./scripts/demo.sh stage capacity-demand
```

Ожидаем: User pool приближается к пределу, задержка растёт, фактический поток
остаётся ниже target.
Для показа достаточно 30–60 секунд устойчивого дефицита; ждать полного
таймаута запросов не нужно.

```bash
# Под действующей нагрузкой добавить dynamic-узлы 3 → 9.
./scripts/demo.sh action scale-9
```

Workload не перезапускается. После переноса таблеток ожидаем девять линий CPU,
около 504 поисковых RPS и восстановление штатной задержки без постоянных ошибок.
Переходить к отказам после трёх последовательных интервалов у target; обычно
переразмещение укладывается примерно в минуту.

Сказать:

> Мы не изменили запросы и не перезапустили workload. Добавили вычислительную
> ёмкость, YDB переразместила таблетки, и тот же профиль достиг полного target.

## 6. Отказоустойчивость

```bash
# Проверить девять узлов и оставить действующую нагрузку 288/144/72 без рестарта.
./scripts/demo.sh stage resilience
```

Каждый отказ и его восстановление выполняются отдельными командами. Между ними
показать в Grafana отметку отказа, p95, ошибки/ретраи и продолжающийся поток.
Следующий отказ применять только после завершения восстановления предыдущего.

```bash
# Временно сделать один диск недоступным YDB.
./scripts/demo.sh fault disk

# Вернуть диск, запустить сервисы и проверить поиск.
./scripts/demo.sh restore disk

# Аварийно завершить процессы YDB на одном узле.
./scripts/demo.sh fault process

# Запустить процессы и проверить поиск.
./scripts/demo.sh restore process

# Остановить все YDB-сервисы одного сервера.
./scripts/demo.sh fault server

# Запустить сервисы и проверить поиск.
./scripts/demo.sh restore server

# Остановить YDB-сервисы условного ДЦ: узлы 7, 8 и 9.
./scripts/demo.sh fault dc

# Запустить YDB-сервисы узлов 7, 8 и 9 и проверить поиск.
./scripts/demo.sh restore dc
```

Команда `fault` возвращает управление сразу после применения отказа. Команда
`restore` снимает именно этот отказ и запускает проверку всех поисковых веток.
Для диска и остановленных сервисов, включая три узла ДЦ, остаётся страховочное
автовосстановление через 10 минут, но штатно ждать его не нужно.

## 7. Завершение и восстановление

```bash
# Остановить только workload; топологию и данные не менять.
./scripts/demo.sh stop
```

Если последовательность была прервана или состояние неизвестно:

```bash
# Остановить workload, восстановить ДЦ/диск/сервер/процесс, вернуть три узла
# и проверить YDB. Команда не пересоздаёт индекс.
./scripts/demo.sh recover
```

Чтобы после аварийного восстановления снова начать демонстрацию с одной
полнотекстовой партиции:

```bash
./scripts/demo.sh prepare
```

Для предварительной проверки без изменений любая команда поддерживает
`--dry-run`:

```bash
./scripts/demo.sh --dry-run stage capacity-base
```

Низкоуровневые команды во время штатного показа не используются. Список
поддерживаемых переходов можно получить через `./scripts/demo.sh --help`.
