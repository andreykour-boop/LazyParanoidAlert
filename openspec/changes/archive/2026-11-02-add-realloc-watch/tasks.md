# Tasks

## 1. Парсер SMART-таблицы (кроссплатформенный)

- [x] 1.1 `parseSMARTAttributes(raw []byte) map[byte]uint64` в `internal/health` (без сборочных тегов): версия в байте 0, записи по 12 байт (id, флаги 2, значение 1, raw 8 LE), id → raw-значение; короткий/невалидный blob → пустая карта. Тесты: фиксируемый blob с атрибутами 5/194/197, обрезанный blob, пустой вход
- [x] 1.2 Хелпер значения счётчика из raw-байтов (uint64 из первых байтов raw, little-endian) с тестом

## 2. Windows: счётчики в L1

- [x] 2.1 Чтение `MSStorageDriver_FailurePredictData` через существующий `wmiForEach` (namespace `root\wmi`): InstanceName + VendorSpecific; injectable-интерфейс (query-функция) для тестов, как у `physicalDiskSource`
- [x] 2.2 Сопоставление с дисками: `Win32_DiskDrive` (SerialNumber ↔ PNPDeviceID ↔ префикс InstanceName), резерв — нормализованная модель; заливка `Realloc`/`Pending` в записи `physicalDiskSource` по серийнику; несопоставленным дискам счётчики не присваиваются. Тесты: fixture blob (TOSHIBA с realloc 481), сопоставление, диск без SMART-записи остаётся без счётчиков, два одинаковых диска с разными серийниками
- [x] 2.3 Интеграция в `NewPlatformSources` (обогащение после WMI/PowerShell-сбоя fallback — обогатить то, что вернулось); сбой SMART-источника не роняет сбор L1 и не добавляет ошибок в снимок

## 3. Диф и тревога роста realloc

- [x] 3.1 `Diff`: новый вид изменения `realloc_growth` — сравнение по Name+Serial, только увеличение ≥ 1, детализация «realloc 481 → 482»; тесты: рост, без изменений, первый снимок, уменьшение игнорируется, диск исчез/появился — не тревога роста
- [x] 3.2 Формат сообщения тревоги в runner (текст через существующий `formatChanges`), сквозной тест runner: рост между проверками → одна тревога; стабилизация → тишина

## 4. Полный контур и документация

- [x] 4.1 `gofmt`, `go vet ./...`, `go test ./... -count=1` — всё зелёное (тесты cmd/siren — приём с временно отодвинутым .syso); `build.ps1` для GOOS=linux проходит без правок
- [x] 4.2 Пересборка дистрибутивов (`dist/siren-win-kit`, `dist/siren-linux-kit`, zip) и построчное дополнение README: счётчики в отчётах/панели, тревога на рост realloc
- [x] 4.3 Живая проверка на Windows-машине: `-health-report`/панель показывают realloc/pending для SATA-дисков (AMD SSD, TOSHIBA), Seagate Expansion — без цифр и без ошибок; при следующей плановой проверке 09:00 — отсутствие ложных тревог. Проверка подтверждена владельцем (TOSHIBA: «realloc 100, pending 100»; AMD SSD/NVMe и Seagate/USB — без цифр и без ошибок). Пост-проверочная сверка со smartctl 7.5 выявила WMI-семантику normalized-вместо-raw — см. design Risks; подключение smartctl как основного источника вынесено в следующий change.
