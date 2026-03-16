package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"text/template"

	"github.com/pufanyi/opencode-manager/internal/config"
)

const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorRed    = "\033[0;31m"
)

func Run(outputPath string) error {
	reader := bufio.NewReader(os.Stdin)
	stepNum = 0

	printBanner()

	// Step 1: Telegram Bot Token
	token, err := stepToken(reader)
	if err != nil {
		return err
	}

	// Step 2: Allowed Users
	users, err := stepUsers(reader)
	if err != nil {
		return err
	}

	// Step 3: OpenCode Binary
	binary, err := stepBinary(reader, "opencode", "OpenCode")
	if err != nil {
		return err
	}

	// Step 4: Claude Code Binary
	claudeBinary, err := stepBinary(reader, "claude", "Claude Code")
	if err != nil {
		return err
	}

	// Step 5: Port Range
	portStart, portEnd, err := stepPorts(reader)
	if err != nil {
		return err
	}

	// Step 6: Database Path
	dbPath, err := stepDatabase(reader)
	if err != nil {
		return err
	}

	// Step 7: Projects
	projects, err := stepProjects(reader)
	if err != nil {
		return err
	}

	// Handle existing file
	if outputPath == "" {
		outputPath = "opencode-manager.yaml"
	}
	if _, err := os.Stat(outputPath); err == nil {
		overwrite := prompt(reader, colorYellow+outputPath+" already exists. Overwrite? [y/N]: "+colorReset)
		if !strings.HasPrefix(strings.ToLower(overwrite), "y") {
			outputPath = "opencode-manager.new.yaml"
			fmt.Printf("  Writing to %s instead.\n", outputPath)
		}
	}

	// Ensure parent directory exists
	if dir := filepath.Dir(outputPath); dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}

	// Write config from template
	data := configData{
		Token:        token,
		Users:        users,
		Binary:       binary,
		ClaudeBinary: claudeBinary,
		PortStart:    portStart,
		PortEnd:      portEnd,
		DBPath:       dbPath,
		Projects:     projects,
	}
	if err := writeConfig(outputPath, data); err != nil {
		return err
	}

	printDone(outputPath)
	return nil
}

// ── Steps ──────────────────────────────────────────────────────────────────

func stepToken(r *bufio.Reader) (string, error) {
	stepNum++
	printStep(stepNum, 7, "Telegram Bot Token")
	hint("Create a bot via @BotFather on Telegram to get your token.")

	for {
		tok := prompt(r, "  Bot token: ")
		parts := strings.SplitN(tok, ":", 2)
		if len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0 {
			if _, err := strconv.Atoi(parts[0]); err == nil {
				return tok, nil
			}
		}
		printErr("Invalid format. Expected: 123456789:ABCdef...")
	}
}

func stepUsers(r *bufio.Reader) ([]int64, error) {
	stepNum++
	printStep(stepNum, 7, "Allowed Telegram User IDs")
	hint("Send /start to @userinfobot on Telegram to find your user ID.")

	var users []int64
	for {
		s := prompt(r, "  User ID (empty to finish): ")
		if s == "" {
			break
		}
		id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			printErr("Must be a number.")
			continue
		}
		users = append(users, id)
		printOK("Added user %d", id)
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("at least one user is required")
	}
	fmt.Println()
	return users, nil
}

var stepNum int

func stepBinary(r *bufio.Reader, defaultName, label string) (string, error) {
	stepNum++
	printStep(stepNum, 7, label+" Binary Path")

	detected := ""
	if p, err := exec.LookPath(defaultName); err == nil {
		detected = p
		printOK("Detected: %s", detected)
	}

	def := defaultName
	if detected != "" {
		def = detected
	}

	val := prompt(r, fmt.Sprintf("  Binary path [%s]: ", def))
	if val == "" {
		val = def
	}

	if _, err := exec.LookPath(val); err != nil {
		if _, err := os.Stat(val); err != nil {
			printWarn("'%s' not found. Make sure it's installed before running.", val)
		}
	} else {
		printOK("OK")
	}
	fmt.Println()
	return val, nil
}

func stepPorts(r *bufio.Reader) (int, int, error) {
	stepNum++
	printStep(stepNum, 7, "Port Range")
	hint("Range of ports for OpenCode instances (1 port per instance).")

	startStr := prompt(r, "  Start port [14096]: ")
	start := 14096
	if startStr != "" {
		if v, err := strconv.Atoi(startStr); err == nil {
			start = v
		}
	}

	endStr := prompt(r, "  End port [14196]: ")
	end := 14196
	if endStr != "" {
		if v, err := strconv.Atoi(endStr); err == nil {
			end = v
		}
	}

	if start >= end || start < 1024 || end > 65535 {
		printWarn("Invalid range, using defaults 14096-14196.")
		start, end = 14096, 14196
	}

	printOK("%d slots available", end-start)
	fmt.Println()
	return start, end, nil
}

func stepDatabase(r *bufio.Reader) (string, error) {
	stepNum++
	printStep(stepNum, 7, "Database Path")

	val := prompt(r, "  SQLite database path [./data/opencode-manager.db]: ")
	if val == "" {
		val = "./data/opencode-manager.db"
	}

	dir := filepath.Dir(val)
	if err := os.MkdirAll(dir, 0755); err != nil {
		printWarn("Could not create directory %s: %v", dir, err)
	}

	fmt.Println()
	return val, nil
}

func stepProjects(r *bufio.Reader) ([]config.ProjectConfig, error) {
	stepNum++
	printStep(stepNum, 7, "Pre-register Projects (optional)")
	hint("Add projects to auto-manage. Leave name empty to skip/finish.")

	var projects []config.ProjectConfig
	for {
		name := prompt(r, "  Project name: ")
		if name == "" {
			break
		}

		dir := prompt(r, "  Project directory: ")
		if dir == "" {
			printErr("Directory required, skipping.")
			continue
		}

		// Expand ~
		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[2:])
		}

		// Resolve to absolute
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}

		if info, err := os.Stat(dir); err != nil {
			printWarn("Directory does not exist yet.")
		} else if !info.IsDir() {
			printWarn("Path is not a directory.")
		} else {
			printOK("Resolved: %s", dir)
		}

		provStr := prompt(r, "  Provider (claudecode/opencode) [claudecode]: ")
		prov := "claudecode"
		if strings.HasPrefix(strings.ToLower(provStr), "o") {
			prov = "claudecode"
		}

		autoStr := prompt(r, "  Auto-start on boot? [y/N]: ")
		autoStart := strings.HasPrefix(strings.ToLower(autoStr), "y")

		projects = append(projects, config.ProjectConfig{
			Name:      name,
			Directory: dir,
			AutoStart: autoStart,
			Provider:  prov,
		})
		printOK("Added project '%s'", name)
		fmt.Println()
	}

	fmt.Println()
	return projects, nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func prompt(r *bufio.Reader, msg string) string {
	fmt.Print(msg)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func printBanner() {
	fmt.Println(colorBold + colorCyan)
	fmt.Println("  ┌──────────────────────────────────────┐")
	fmt.Println("  │     OpenCode Manager Setup Wizard    │")
	fmt.Println("  └──────────────────────────────────────┘")
	fmt.Println(colorReset)
}

func printStep(n, total int, title string) {
	fmt.Printf("%sStep %d/%d: %s%s\n", colorBold, n, total, title, colorReset)
}

func hint(msg string) {
	fmt.Printf("  %s%s%s\n", colorYellow, msg, colorReset)
}

func printOK(format string, args ...any) {
	fmt.Printf("  %s✓ %s%s\n", colorGreen, fmt.Sprintf(format, args...), colorReset)
}

func printWarn(format string, args ...any) {
	fmt.Printf("  %s⚠ %s%s\n", colorYellow, fmt.Sprintf(format, args...), colorReset)
}

func printErr(format string, args ...any) {
	fmt.Printf("  %s✗ %s%s\n", colorRed, fmt.Sprintf(format, args...), colorReset)
}

func printDone(cfgPath string) {
	bin := os.Args[0]
	fmt.Println(colorBold + colorGreen)
	fmt.Println("  ┌──────────────────────────────────────┐")
	fmt.Println("  │           Setup Complete!             │")
	fmt.Println("  └──────────────────────────────────────┘")
	fmt.Println(colorReset)
	fmt.Printf("  Config written to: %s%s%s\n\n", colorCyan, cfgPath, colorReset)
	fmt.Println("  " + colorBold + "Next steps:" + colorReset)
	fmt.Println()
	fmt.Printf("  1. Run:\n")
	fmt.Printf("     %s%s -config %s%s\n\n", colorCyan, bin, cfgPath, colorReset)
	fmt.Printf("  2. Open Telegram and send %s/start%s to your bot.\n\n", colorCyan, colorReset)
	fmt.Printf("  3. Create an instance:\n")
	fmt.Printf("     %s/new myproject /path/to/project%s\n\n", colorCyan, colorReset)
	fmt.Printf("  4. Send any message to prompt it!\n\n")
}

// ── Config template ────────────────────────────────────────────────────────

type configData struct {
	Token        string
	Users        []int64
	Binary       string
	ClaudeBinary string
	PortStart    int
	PortEnd      int
	DBPath       string
	Projects     []config.ProjectConfig
}

var configTmpl = template.Must(template.New("config").Parse(`telegram:
  token: "{{ .Token }}"
  allowed_users: [{{ range $i, $u := .Users }}{{ if $i }}, {{ end }}{{ $u }}{{ end }}]

process:
  opencode_binary: "{{ .Binary }}"
  claudecode_binary: "{{ .ClaudeBinary }}"
  port_range:
    start: {{ .PortStart }}
    end: {{ .PortEnd }}
  health_check_interval: 30s
  max_restart_attempts: 3
{{ if .Projects }}
projects:{{ range .Projects }}
  - name: "{{ .Name }}"
    directory: "{{ .Directory }}"
    provider: "{{ .Provider }}"
    auto_start: {{ .AutoStart }}{{ end }}
{{ else }}
projects: []
{{ end }}
storage:
  database: "{{ .DBPath }}"
`))

func writeConfig(path string, data configData) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("creating config file: %w", err)
	}
	defer f.Close()

	if err := configTmpl.Execute(f, data); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}
