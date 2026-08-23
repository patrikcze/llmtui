module github.com/patrikcze/llmtui

go 1.27.0

// Keep libffi lazy so non-embedded providers can start on Linux without libffi.so.8.
replace github.com/jupiterrider/ffi => ./third_party/ffi

require (
	charm.land/bubbles/v2 v2.2.0
	charm.land/bubbletea/v2 v2.0.9
	charm.land/glamour/v2 v2.0.1
	charm.land/lipgloss/v2 v2.0.6
	github.com/JohannesKaufmann/html-to-markdown/v2 v2.5.2
	github.com/ardanlabs/jinja v1.1.0
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/ebitengine/purego v0.10.2
	github.com/go-shiori/go-readability v0.0.0-20251205110129-5db1dc9836f0
	github.com/hybridgroup/yzma v1.24.0
	github.com/jupiterrider/ffi v0.7.0
	github.com/lrstanley/bubblezone/v2 v2.0.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/viper v1.21.0
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.47.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/JohannesKaufmann/dom v0.3.1 // indirect
	github.com/alecthomas/chroma/v2 v2.20.0 // indirect
	github.com/andybalholm/cascadia v1.3.4 // indirect
	github.com/araddon/dateparse v0.0.0-20210429162001-6b43995a97de // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260811164956-006e29f97886 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20250327172914-2fdc97757edf // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-shiori/dom v0.0.0-20230515143342-73569d674e1c // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/gogs/chardet v0.0.0-20211120154057-b7413eaefb8f // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yuin/goldmark v1.8.2 // indirect
	github.com/yuin/goldmark-emoji v1.0.6 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)
