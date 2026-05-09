# tidy

A terminal UI for finding and removing build artefacts and dependency caches across local projects.

It walks a directory tree, groups what it finds by language, and shows total disk usage. You deselect anything you want to keep, then delete the rest. Results are cached so reopening is fast; press `r` to rescan.

## Install

```
go install github.com/iamkoch/tidy-code@latest
```

The installed binary is `tidy-code`. Alias to `tidy` if you prefer:

```
alias tidy=tidy-code
```

## Usage

```
tidy-code              # scans the current directory
tidy-code ~/projects   # scans the given path
```

All items start selected. Toggle off the ones you want to keep, then press `d`.

## Screenshots

The TUI uses ANSI colour, which markdown does not render. The blocks below show layout and structure; in a real terminal the cursor row is highlighted, selected items are green, partially selected groups are amber, and the last-modified column is coloured red, amber, or green by age.

Initial scan:

```
  ⠋ scanning /Users/ant/projects for build artefacts...
```

List view (cursor on the Node.js header, one item under Node.js deselected, Rust group partially selected):

```
  tidy   /Users/ant/projects
  selected: 18.3 GiB across 142 items
  cached 2h ago · press [r] to rescan

▸ [x] ▾ Node.js (87)              12.4 GiB / 14.1 GiB
      [x]   412.3 MiB   2w ago      /Users/ant/projects/web-app/node_modules
      [x]   388.1 MiB   3d ago      /Users/ant/projects/api/node_modules
      [ ]    92.4 MiB   5h ago      /Users/ant/projects/feature-x/node_modules
      [x]    45.8 MiB   2mo ago     /Users/ant/projects/old-prototype/node_modules
  [~] ▾ Rust (4)                     3.1 GiB / 4.8 GiB
      [x]     2.8 GiB   1mo ago     /Users/ant/projects/compiler-fork/target
      [ ]     1.7 GiB   2d ago      /Users/ant/projects/active-rs-app/target
  [x] ▸ .NET (12)                    1.6 GiB / 1.6 GiB
  [x] ▸ Python (39)                847.2 MiB / 847.2 MiB

  [↑↓/jk] move  [PgUp/PgDn] page  [g/G] top/bot  [Tab/[ ]] next/prev group
  [c/o or ←/→] fold  [C/O] fold all  [space] toggle  [a/n] all/none  [d] delete  [r] rescan  [q] quit
```

Confirmation:

```
  tidy

  Delete 142 items totalling 18.3 GiB?
  This cannot be undone.

  [y] yes   [n] no
```

After delete, back to the list with a status line:

```
  tidy   /Users/ant/projects
  selected: 0 B across 0 items
  freed 18.3 GiB across 142 items

  nothing to clean up.

  [q] quit
```

Glyph key:

| Glyph | Meaning |
| --- | --- |
| `▸` | cursor / collapsed group |
| `▾` | expanded group |
| `[x]` | selected (green) |
| `[~]` | partially selected group (amber) |
| `[ ]` | deselected |
| `-` | mtime unknown |

## Keys

| Action | Key |
| --- | --- |
| Move | `↑/↓`, `j/k` |
| Page | `PgUp/PgDn`, `Ctrl-U/D` |
| Top, bottom | `g`, `G` |
| Next, previous group | `Tab`, `Shift+Tab` (also `]`, `[`) |
| Collapse current group | `c`, `←`, `h` |
| Expand current group | `o`, `→`, `l` |
| Collapse all, expand all | `C`, `O` |
| Toggle row | `Space`, `x` |
| Select all, none | `a`, `n` |
| Delete selected | `d` (then `y` to confirm) |
| Rescan | `r` |
| Quit | `q`, `Ctrl-C` |

Toggling a group row toggles every item beneath it.

## Last-modified colours

Each artefact shows the most recent mtime found anywhere beneath it.

| Colour | Age |
| --- | --- |
| Red | within the past week |
| Amber | one to four weeks |
| Green | older than four weeks |

The point is to give a quick read on which artefacts are tied to active work.

## What it finds

| Language | Directories | Marker required in parent |
| --- | --- | --- |
| Node.js | `node_modules`, `.next`, `.nuxt`, `.turbo`, `.parcel-cache`, `.yarn` | none |
| .NET | `bin`, `obj` | `*.csproj`, `*.fsproj`, `*.vbproj`, `*.sln` |
| Rust | `target` | `Cargo.toml` |
| Java (Maven) | `target` | `pom.xml` |
| Java (Gradle) | `build`, `.gradle` | `build.gradle` (for `build` only) |
| Python | `__pycache__`, `.venv`, `venv`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.tox` | none |
| Go | `vendor` | `go.mod` |
| PHP | `vendor` | `composer.json` |
| Ruby | `.bundle` | none |
| Elixir | `_build`, `deps` | `mix.exs` |
| Swift / Xcode | `DerivedData`, `.swiftpm`, `.build` | `Package.swift` (for `.build` only) |
| Dart / Flutter | `.dart_tool` | none |
| C / C++ | `cmake-build-debug`, `cmake-build-release` | none |

The marker check reuses directory entries already loaded for the parent walk, so detection costs no extra `ReadDir` calls.

## How it works

Two phases run concurrently. They share one semaphore that caps concurrent `ReadDir` syscalls so the disk does not get hammered.

1. Find walk: one goroutine per directory. When a directory name matches a rule and any required marker is alongside, the subtree is queued for sizing and not descended into.
2. Size walk: same fan-out for each artefact, summing `info.Size()` into an atomic counter and tracking the most recent mtime seen.

A 22 GiB tree of around 4,000 artefacts on an Apple M4 Pro completes in roughly 13 seconds cold, bound by FS syscall throughput.

Symlinks are not followed during sizing. `.git`, `.hg`, and `.svn` are skipped.

## Cache

Results are written to `~/Library/Caches/tidy/<sha256-prefix>.json` (or whatever `os.UserCacheDir` returns on your platform). The absolute root path is stored inside the file and verified on load, so a hash collision cannot load the wrong cache. After a successful delete the cache is rewritten.

Only paths that `os.RemoveAll` actually removed are dropped from the model. Failed deletes stay visible and selected so you can retry.

## Build from source

```
git clone git@github.com:iamkoch/tidy-code.git
cd tidy-code
go install .
```

Tests:

```
go test -race ./...
```

There is a benchmark you can point at a real tree:

```
TIDY_BENCH_ROOT=~/code go test -bench=BenchmarkScan -benchtime=1x -run=^$ ./...
```

## License

MIT. See `LICENSE`.
