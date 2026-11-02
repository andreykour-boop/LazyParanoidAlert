# Proposal

## Why

Сторож здоровья дисков видит счётчики секторов не на всех машинах: на Windows WMI-путь отдаёт счётчики только SATA-дискам (NVMe и USB — честное отсутствие), а живая сверка со smartctl 7.5 показала, что WMI-драйвер на реальной машине кладёт в raw-позиции нормализованное значение («здоровье %»), а не счётчик секторов — при таком семантике тревога `realloc_growth` на этой машине не сработает в принципе. smartmontools 7.5 уже установлен на обеих машинах (Windows и Linux-сервер), и smartctl отдаёт честные raw-значения для SATA и полный журнал здоровья NVMe (media errors, wear). Решение принимается зондированием подсистемы на живой машине при запуске, а не выбором «под один вид железа»: инструменты coexistят, побеждает более точный источник.

## What Changes

- Новый smartctl-источник на Windows: перечень устройств из `smartctl --scan`, сбор `smartctl -i -H -A -j`, сопоставление с дисками по серийнику; становится основным источником счётчиков для SATA и NVMe.
- Существующее WMI-обогащение (`root\wmi\MSStorageDriver_FailurePredictData`) становится фолбэком на Windows: используется, только если smartctl не найден.
- NVMe-счётчики на обеих платформах: `media_errors` (Media and Data Integrity Errors) и `wear` (Percentage Used, %) парсятся из JSON-вывода smartctl (`-j`); новый рендер «— media errors N, wear N%».
- Новая тревога `media_error_growth`: рост media_errors ≥ 1 между проверками — немедленная, по аналогии с `realloc_growth`.
- Провенанс счётчиков: запись диска хранит источник счётчиков; при смене источника между проверками diff молча перебазирует счётчики (не сравнивает значения через границу источников — защита от ложных тревог вида «WMI normalized 100 vs smartctl raw 0»).
- `findTool("smartctl")` на Windows дополняется кандидатами путей (`C:\Program Files\smartmontools\bin`) — служба (LocalSystem) не видит пользовательский PATH, куда NSIS-установщик прописывает smartmontools.
- Текстовый парсер smartctl остаётся для vendor-cli пути (диски за RAID-контроллерами) без изменений.

## Capabilities

### New Capabilities

(нет — поведение расширяется внутри существующей capability)

### Modified Capabilities

- `disk-health-watch`: требование «Счётчики секторов SMART в снимке L1» меняется — smartctl становится основным источником счётчиков на Windows (WMI — фолбэк), добавляются NVMe-счётчики media_errors/wear; новое требование о немедленной тревоге на рост media_errors; дополнение о перебазировании счётчиков при смене источника.

## Impact

- `internal/health/`: новый smartctl-источник для Windows (`--scan`, JSON-парсер), `parse.go` (текстовый парсер — без изменений, для vendor-cli), `model.go` (поля `MediaErrors`/`Wear` + источник счётчиков), `diff.go` (kind `media_error_growth`, перебазирование при смене источника), `report.go` (рендер NVMe-счётчиков), `collectors.go` (`findTool` — кандидаты путей).
- Внешняя зависимость: smartmontools (уже установлен на обеих машинах; отсутствие — не ошибка, честное «не смог проверить»).
- State-файл: новые поля с `omitempty` — обратно совместим; поле источника счётчиков добавляется без миграции.
- Права: smartctl требует элевации на Windows — служба (LocalSystem) и пульт (requireAdministrator) имеют доступ, как и с WMI.
