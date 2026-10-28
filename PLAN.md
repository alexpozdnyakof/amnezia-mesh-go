# План разработки awg-mesh-go

Дочерний план к plan.md (Эпик 1). Обновлено: 2026-10-23.

## Принципы (фиксируем до старта)

1. **1:1 порт, редизайна нет.** Python-версия — reference impl, по завершении замораживается в пользу Go как canonical.
2. **Прод не трогаем.** Никаких прогонов против живого `amnezia-mesh-awg`: не останавливаем, не перезапускаем, не меняем его конфиг. Все автотесты — на фикстурах. Live-проверки — только на параллельном стенде (см. этап 4).
3. **Threat-intel вне скоупа.** Отдельный сервис на проде, подключён только к текущему конфигу. Контракт «restart после peer-операций» не переносим и не проверяем.
4. **Ключи — тем же способом, что Python:** `docker run --entrypoint awg` (`awg_mesh/keygen.py:38–65`). Крипто-зависимостей в Go нет, риск несовместимости ключей отсутствует.

## Зафиксированные решения

1. **Зависимости:** stdlib + `golang.org/x/sys/unix` (только для flock). Статический бинарь сохраняется.
2. **Parity = байтовое сравнение артефактов**, не «тесты проходят».
3. **Детерминизм:** рандом и время пинним (см. этап 4).
4. **CI — в бэклог**, настраиваем после релизной механики.
5. **Размещение:** соседняя директория `amnezia-mesh-go` (рядом с `amnezia-mesh`), отдельный git-репозиторий, module `github.com/alexpozdnyakof/amnezia-mesh-go`. Фикстуры копируем в репо (self-contained); путь к Python-чекауту parity-скрипт берёт из `AWG_MESH_PY` (по умолчанию `../amnezia-mesh`).
6. Threat-intel — п. 3 принципов.
7. **Adversarial-фикстуры кодека добавляем:** unicode-имена устройств, H-параметры около 2³², граничные случаи base64-паддинга.
8. **Параллельный стенд для live-тестов:** отдельный compose-проект (`docker compose -p awg-mesh-test` или аналог) с непересекающимися подсетью, портом и именами контейнеров. Продовый контейнер и его конфиг не изменяются ни разу за весь проект.

## Скоуп портирования

| Python (awg_mesh/) | Строк | Go (amnezia-mesh-go) |
|---|---|---|
| envfile.py | 26 | internal/envfile |
| peers.py (+flock) | 109 | internal/peers (flock через x/sys/unix.Flock, держим на всю запись, как `peers.py:78`) |
| render.py | 207 | internal/render |
| profile.py (vpn:// codec) | 226 | internal/profile |
| keygen.py (docker exec) | 102 | internal/keygen (os/exec) |
| compose.py | 15 | internal/compose |
| doctor.py | 89 | internal/doctor |
| adopt.py | 218 | internal/adopt |
| cli.py | 515 | cmd/awg-mesh |
| tests/ + fixtures | 528 | tests/ (те же фикстуры + новые adversarial) |

Команды CLI: `init`, `adopt`, `peer add/list/revoke/rename`, `doctor`, `monitoring`.

## Этапы

**Этап 0 — скелет (0.5 дня).** git init, go.mod, layout `cmd/awg-mesh` + `internal/`, Makefile, копия фикстур в репо.

**Этап 1 — envfile + peers + render (2–4 дня).** Портируем три чистых файловых модуля, golden-тесты — на скопированных фикстурах (`awg0.conf`, `dnsmasq.conf`, `start.sh`). Критерий: `go test` зелёный на тех же входах, на которых зелёный pytest.

**Этап 2 — profile codec (1–2 дня).** vpn:// (zlib + base64 + json) roundtrip против фикстур + adversarial-фикстуры (решение 7). Закрывает риск zlib-совместимости Python↔Go.

**Этап 3 — keygen, compose, doctor, adopt, cli (3–5 дня).** Оставшиеся модули и обвязка всех команд. keygen — os/exec на docker, как в Python (принцип 4). doctor/adopt в unit-тестах — с моком exec.

**Этап 4 — parity-harness + стенд (1–2 дня).** `make parity`:
- скрипт гоняет Python CLI (из `AWG_MESH_PY`) и Go-бинарь на временной копии фикстур;
- детерминизм: рандом — сидируемый генератор через `AWG_MESH_SEED`, время — injectable clock (в prod — реальное, в тестах — фиксированное);
- сравнение: байтовый diff артефактов (`awg0.conf`, `docker-compose.yml`, `peers.db`, `server.env`) + stdout/stderr/exit-коды по всем командам.
- Live-проверка doctor/adopt — на параллельном стенде (решение 8), прод не трогаем.
Критерий: нулевой diff на полном наборе команд.

**Этап 5 — релизная механика (1 день).** `.goreleaser.yml` (linux/darwin × amd64/arm64), тег, GitHub Releases, brew tap.

**Этап 6 — restart-free apply (сделано, e1395c0 / python eba17a8).** `peer add` — инкрементальный `awg set` + SIGHUP dnsmasq (`peers.hosts` через `--addn-hosts`); `peer rename` — только SIGHUP; `peer revoke` — restart (userspace wedge, см. комментарий в cli).

**Этап 7 — remote mode, дизайн A «thin proxy» (запланировано).** Админка с мака: `--server <ssh-alias>` / `AWG_MESH_SSH` + `AWG_MESH_REMOTE_ROOT`. Семантика: маковский CLI не дублирует логику — он исполняет `ssh <alias> 'cd <root> && awg-mesh <args>'` системным ssh-клиентом (ключи из ssh-config пользователя), а после мутирующих команд вытягивает `config/peers/` обратно scp, чтобы `.vpn`-профили оказались локально. На сервере требуется любой awg-mesh CLI (python или go). Дизайн B (`DOCKER_HOST=ssh://`) отвергнут: bind-mounts резолвятся на удалённой стороне, половина doctor'овских проверок локальна. Связь с TUI (`amnezia-mesh-tui`): панель имеет собственный ssh-транспорт (shell-out, `transport.rs`) и на эту фичу не зависит; remote mode — CLI-эквивалент для терминала и агентов. Реализация в обоих репо в lockstep + parity-шаг 14 (ssh-заглушка в harness).

Итого: ~1,5–2 недели фокусной работы.

## Критерий приёмки

- `go test ./...` зелёный: перенесённые golden + codec roundtrip + adversarial.
- `make parity` — нулевой diff против Python CLI.
- doctor/adopt проверены на параллельном стенде; продовый контейнер и его конфиг не изменялись ни разу.
- После приёмки Python объявляется reference impl и замораживается.

## Бэклог

- **CI (сделано):** GitHub Actions — `test` (gofmt/vet/go test) + `parity` (13 шагов против Python-репо) на push/PR; `release.yml` — goreleaser по тегу `v*`.
- **README Tier-3 переводы** (ko, fr, hi, id, uk) одним проходом под релиз amnezia-mesh-go, с синхронизацией всего существующего набора (из мастер-плана).
- bulk rename auto-generated имён (мелочь).
- Опционально: fuzz profile codec.

## Риски

- **zlib-совместимость** Python↔Go — закрывается roundtrip-фикстурами (этап 2).
- **Семантика flock:** fcntl (Python) vs unix.Flock (Go) — в Go держать эксклюзивный lock на всю запись, как `peers.py:78`, без переоткрытий между чтением и записью.
- **doctor/adopt завязаны на live-состояние** (docker exec, сеть) — в unit-тестах мокаем exec; живое поведение — только на параллельном стенде (этап 4).
- **Таймстемпы** в render/profile — все источники времени проходят через clock-интерфейс, иначе parity-прогон недетерминирован.
