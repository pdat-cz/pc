# Changelog

## [0.3.2](https://github.com/pdat-cz/pc/compare/v0.3.1...v0.3.2) (2026-03-21)

### Bug Fixes

* **mbus:** multiplier silently discarded for any FD/FB record with a VIFE chain — bit 7 of secondary VIF means "more VIFEs follow", not "ignore scaling"; affected voltage, current, credit and other scaled records ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **mbus:** Type F datetime year corrupted by +64 years when device reports DST/summer-time flag ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **mbus:** voltage and current ranges swapped in FD extension table (0x40–0x4F = voltage, 0x50–0x5F = current per EN 13757-3) ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **mbus:** FD extension missing entries: 0x1E retry, 0x23 tariff_start, 0x30 duration_of_tariff (seconds) ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **mbus:** averaging_duration (0x70–0x73) and actuality_duration (0x74–0x77) were decoded as the same quantity ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **cat:** context timeout hard-coded at 3 s regardless of address count or baud rate; now scales as `3s + per-baud-timeout × address-count` ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **scan:** missing inter-frame gap between address probes caused frame collisions on busy M-Bus segments ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **mbus client:** pipe reader goroutine not started for `cat`, not stopped on `Close()` ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))

### Features

* **mbus:** full EN 13757-3 Annex C (FB extension) VIF table — extended energy/volume/mass/power ranges, US/imperial units, temperatures in °F, cumul_max_power ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **mbus:** unknown VIF codes now emit `"quantity": "unknown"` + `"vif_raw": "0x7D"` instead of opaque `vif_0x7D` — downstream consumers can filter on `"unknown"` as a class ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))
* **scan:** suggested `pc cat` command after scan now includes all serial parameters explicitly ([f85f075](https://github.com/pdat-cz/pc/commit/f85f075))

### ⚠ JSON Output Changes

The M-Bus record JSON format has changed in this release:

| Before | After | Note |
|--------|-------|------|
| `"function": "instantaneous"` | `"data_function": "instantaneous"` | field renamed |
| `"function": "error_state"` | `"data_function": "error_value"` | renamed; value is valid M-Bus data, not a parse error |
| `"value": "0x1A (decode error: ...)"` | `"value": null, "error": "0x1A: ..."` | errors moved to separate field |
| `"storage_no"` omitted when 0 | always emitted | absent-means-zero was ambiguous |
| `"tariff"` omitted when 0 | always emitted | absent-means-zero was ambiguous |
| `"unit": "C"` | `"unit": "°C"` | degree symbol added consistently |

---

## [0.1.6](https://github.com/pdat-cz/pc/compare/v0.1.5...v0.1.6) (2025-09-25)


### Bug Fixes

* actions ([183fed4](https://github.com/pdat-cz/pc/commit/183fed4360b82c49770a56b195d16d2fc2052933))

## [0.1.5](https://github.com/pdat-cz/pc/compare/v0.1.4...v0.1.5) (2025-09-25)


### Bug Fixes

* install.sh ([78ed905](https://github.com/pdat-cz/pc/commit/78ed905dfc63cb29a4ef9f88e72de3b158635031))

## [0.1.4](https://github.com/pdat-cz/pc/compare/v0.1.3...v0.1.4) (2025-09-25)


### Bug Fixes

* fix github actions ([e57e167](https://github.com/pdat-cz/pc/commit/e57e1674ebdd74b90bb3356842cbc3a09161967d))
