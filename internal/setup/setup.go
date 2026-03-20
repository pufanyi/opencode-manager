package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/store"
)

const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorRed    = "\033[0;31m"
)

const totalSteps = 5

// Run runs the interactive setup wizard, writing config to the given store.
func Run(st store.Store) error {
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

	// Build config and save to DB
	cfg := config.Defaults()
	cfg.Telegram.Token = token
	cfg.Telegram.AllowedUsers = users
	cfg.Process.OpencodeBinary = binary
	cfg.Process.ClaudeCodeBinary = claudeBinary
	cfg.Process.PortRange.Start = portStart
	cfg.Process.PortRange.End = portEnd

	if err := st.SetSettings(cfg.ToSettings()); err != nil {
		return fmt.Errorf("saving settings to database: %w", err)
	}

	printDone()
	return nil
}

// ── Steps ──────────────────────────────────────────────────────────────────

func stepToken(r *bufio.Reader) (string, error) {
	stepNum++
	printStep(stepNum, totalSteps, "Telegram Bot Token")
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
	printStep(stepNum, totalSteps, "Allowed Telegram User IDs")
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
	printStep(stepNum, totalSteps, label+" Binary Path")

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
	printStep(stepNum, totalSteps, "Port Range")
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

func printDone() {
	bin := os.Args[0]
	fmt.Println(colorBold + colorGreen)
	fmt.Println("  ┌──────────────────────────────────────┐")
	fmt.Println("  │           Setup Complete!             │")
	fmt.Println("  └──────────────────────────────────────┘")
	fmt.Println(colorReset)
	fmt.Println("  Settings saved to database.")
	fmt.Println()
	fmt.Println("  " + colorBold + "Next steps:" + colorReset)
	fmt.Println()
	fmt.Printf("  1. Run:\n")
	fmt.Printf("     %s%s%s\n\n", colorCyan, bin, colorReset)
	fmt.Printf("  2. Open Telegram and send %s/start%s to your bot.\n\n", colorCyan, colorReset)
	fmt.Printf("  3. Create an instance:\n")
	fmt.Printf("     %s/new myproject /path/to/project%s\n\n", colorCyan, colorReset)
	fmt.Printf("  4. Send any message to prompt it!\n\n")
}
