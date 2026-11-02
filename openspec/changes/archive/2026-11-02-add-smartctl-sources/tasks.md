# Tasks

## 1. Модель и провенанс

- [x] 1.1 `L1Disk`: поля `MediaErrors int64`, `Wear int64` (NVMe), `CountersSrc string` (источник счётчиков) — все с `json:"...,omitempty"`; состояние (сериализация state-файла) обратно совместимо, старый файл без `CountersSrc` читается. Тест: маршилинг/анмаршалинг снимка со старым JSON (без новых полей) и с новыми
- [x] 1.2 `findTool` на Windows: после промаха `LookPath` — перебор кандидатов (`C:\Program Files\smartmontools\bin\smartctl.exe`) с проверкой `os.Stat`; на Linux поведение не меняется. Тест: инжектируемый список кандидатов — существующий файл находится, несуществующий даёт пустую строку

## 2. Smartctl-источник на Windows (JSON)

- [x] 2.1 Структуры JSON smartctl (`-i -H -A -j`): identity (`model_name`, `serial_number`), `smart_status.passed`, `ata_smart_attributes.table` (id 5, 197 → raw), `nvme_smart_health_information_log` (`media_errors`, `percent_used`); устройство — по серийнику (`normSerial`) к L1-записи `physicalDiskSource`; несопоставленные записи отбрасываются. Перечень устройств — `smartctl --scan -j`. Тесты: фиксируемый вывод `smartctl -j` с живой машины (NVMe: media errors 0, wear 11; SATA: attrs 5/197), сопоставление по серийнику, USB-диск из `--scan` без серийника отбрасывается, сбой smartctl не роняет сбор L1 и не добавляет ошибок
- [x] 2.2 Приоритет источников на Windows: smartctl найден и ответил — его счётчики (`CountersSrc = "smartctl"`); smartctl не найден/не ответил — WMI-обогащение (`CountersSrc = "wmi"`); оба пути — на существующих injectable-функциях. Тест: два сценария на фикстурах, поле `CountersSrc` выставляется корректно
- [x] 2.3 Интеграция в `NewPlatformSources` и сборка через `build.ps1` без правок; `gofmt`/`go vet ./...` чистые

## 3. NVMe на Linux

- [x] 3.1 Текстовый `parseSmartctl` (Linux, direct-путь): распознавание «Media and Data Integrity Errors:» и «Percentage Used:» из NVMe-вывода → `MediaErrors`/`Wear`, `CountersSrc = "smartctl"`; vendor-cli путь сохраняет прежнее поведение. Тест: NVMe-фикстура с media errors > 0 (в тексте — с разделителями тысяч: парсер MUST NOT приписывать мусор, поле берётся из JSON-пути direct-источника; текстовый — только для контроля регрессии SATA)

## 4. Диф и тревога

- [x] 4.1 `Diff`: kind `media_error_growth` — рост media_errors ≥ 1 по диску (Name + Serial), только увеличение, detail «диск X: media errors 0 → 3»; формат сообщения в runner — как у дисковых тревог. Тесты: рост, без изменений, первый снимок, уменьшение игнорируется
- [x] 4.2 Перебазирование: если `CountersSrc` диска изменился между снимками (или был пуст в старом state) — счётчики не сравниваются, база фиксируется молча (нет `realloc_growth`, нет `media_error_growth`, нет записей в «не смог проверить»). Тесты: smartctl→wmi (0 → 100 — тишина), wmi→smartctl (100 → 0 — тишина), после перебазирования рост ловится

## 5. Рендеры и документация

- [x] 5.1 `SnapshotText`: строка «— media errors N, wear N%» при заполненных полях (без заполнителей при пустых); панель пульта наследует тот же текст. Тест: снимок с NVMe-диском и с диском без счётчиков — по fixture
- [x] 5.2 Дистрибутивы: `dist/siren-win-kit` и `dist/siren-linux-kit` — README дополняется строками про NVMe-счётчики, smartctl как источник на Windows и WMI-фолбэк; zip пересобран

## 6. Полный контур

- [x] 6.1 `go vet ./...`, `go test ./... -count=1` — всё зелёное (тесты cmd/siren — приём с временно отодвинутым .syso, вернуть на место); пересборка `bin/` через `.\build.ps1`
- [x] 6.2 Живая проверка на Windows-машине: `-health-report`/панель — TOSHIBA со счётчиками из smartctl (realloc/pending = 0/0 — честный raw, не 100/100), AMD SSD NVMe — «media errors 0, wear 11%», Seagate USB — без цифр и без ошибок; `CountersSrc` в state-файле = smartctl. Проверка подтверждена владельцем (16:41, панель: NVMe «ok — media errors 0, wear 11%», TOSHIBA/Seagate — «ok»; state: counters_src smartctl у TOSHIBA и NVMe, тревог нет)

## Workflow follow-up

- Обновить бинарник на сервере 10.5 (`~/siren-kit/siren` + `sudo ./install.sh`) — счётчики NVMe там появятся без правок конфига.
- Архивировать change после ревью (`/opsx-archive`), синхронизировать спеку.
