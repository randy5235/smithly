package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"smithly.dev/internal/agent"
	"smithly.dev/internal/channels"
	"smithly.dev/internal/config"
	"smithly.dev/internal/credentials"
	"smithly.dev/internal/db"
	"smithly.dev/internal/db/sqlite"
	"smithly.dev/internal/gateway"
	"smithly.dev/internal/sidecar"
	"smithly.dev/internal/skills"
	"smithly.dev/internal/store"
	"smithly.dev/internal/tools"
	"smithly.dev/internal/workspace"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		cmdInit()
	case "start":
		cmdStart()
	case "chat":
		cmdChat()
	case "agent":
		cmdAgent()
	case "skill":
		cmdSkill()
	case "oauth2":
		cmdOAuth2()
	case "audit":
		cmdAudit()
	case "doctor":
		cmdDoctor()
	case "version":
		fmt.Println("smithly v0.1.0-dev")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`smithly — secure AI agent runtime

Commands:
  init      First-time setup wizard
  start     Start gateway + agents (HTTP API)
  chat      Interactive terminal chat with an agent
  agent     Manage agents (list, add, remove)
  skill     Manage instruction skills (list, add, remove)
  oauth2    Manage OAuth2 providers (auth, list)
  audit     Show audit log
  doctor    Check dependencies
  version   Print version

Run 'smithly <command> --help' for details.`)
}

// cmdInit runs the first-time setup wizard.
func cmdInit() {
	dir, _ := os.Getwd()
	configPath := filepath.Join(dir, "smithly.toml")

	if _, err := os.Stat(configPath); err == nil {
		fmt.Println("smithly.toml already exists. Delete it to re-initialize.")
		return
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Welcome to Smithly!")
	fmt.Println()

	// Agent name
	fmt.Print("Agent name [assistant]: ")
	agentName, _ := reader.ReadString('\n')
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		agentName = "assistant"
	}

	provider, baseURL, model, apiKey := promptLLMConfig(reader)

	// Search provider (Brave API key)
	fmt.Print("\nBrave Search API key (free at https://brave.com/search/api/, or press Enter to skip): ")
	braveKey, _ := reader.ReadString('\n')
	braveKey = strings.TrimSpace(braveKey)

	// Create workspace directory
	wsPath := filepath.Join("workspaces", agentName)
	os.MkdirAll(filepath.Join(dir, wsPath), 0755)

	// Write default workspace files
	writeIfMissing(filepath.Join(dir, wsPath, "SOUL.md"),
		"You are a helpful, thoughtful AI assistant. You communicate clearly and concisely.")
	writeIfMissing(filepath.Join(dir, wsPath, "IDENTITY.toml"),
		fmt.Sprintf("name = %q\nemoji = \"🤖\"\n", agentName))

	// Write config
	agentCfg := config.AgentConfig{
		ID:        agentName,
		Model:     model,
		Workspace: wsPath,
		Provider:  provider,
		APIKey:    apiKey,
		BaseURL:   baseURL,
	}
	searchCfg := config.SearchConfig{Provider: "brave", APIKey: braveKey}
	if err := config.WriteDefault(dir, agentCfg, searchCfg); err != nil {
		log.Fatalf("Failed to write config: %v", err)
	}

	fmt.Println()
	fmt.Println("Setup complete! Created:")
	fmt.Printf("  smithly.toml\n")
	fmt.Printf("  %s/SOUL.md\n", wsPath)
	fmt.Printf("  %s/IDENTITY.toml\n", wsPath)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  smithly chat    — start chatting with your agent")
	fmt.Println("  smithly start   — start the HTTP gateway")
}

// cmdStart runs the gateway and all agents.
func cmdStart() {
	cfg, dbStore := loadConfig()
	defer dbStore.Close()
	credStore := loadCredentialStore(cfg)

	gw := gateway.New(cfg.Gateway.Bind, cfg.Gateway.Port, cfg.Gateway.Token, cfg.Gateway.RateLimit, dbStore)

	// Start sidecar
	sc := startSidecar(cfg, dbStore, credStore)

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register agents
	for _, ac := range cfg.Agents {
		a, err := loadAgent(ac, cfg, dbStore, credStore)
		if err != nil {
			log.Fatalf("Failed to load agent %s: %v", ac.ID, err)
		}
		gw.RegisterAgent(a)
		log.Printf("registered agent: %s (model: %s)", a.ID, a.Model)

		// Run BOOT.md if present
		if a.Workspace.Boot != "" {
			log.Printf("running BOOT.md for %s...", a.ID)
			if _, err := a.Boot(ctx, nil); err != nil {
				log.Printf("warning: boot for %s failed: %v", a.ID, err)
			}
		}

		// Start heartbeat if configured
		if ac.Heartbeat != nil && ac.Heartbeat.Enabled && a.Workspace.Heartbeat != "" {
			hc := agent.ParseHeartbeatConfig(ac.Heartbeat.Interval, ac.Heartbeat.QuietHours, ac.Heartbeat.AutoResume)
			a.StartHeartbeat(ctx, hc)
			log.Printf("heartbeat started for %s (every %s)", a.ID, hc.Interval)
		}
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		sc.Shutdown(ctx)
		gw.Shutdown(ctx)
		cancel()
	}()

	fmt.Printf("\nGateway: http://%s:%d\n", cfg.Gateway.Bind, cfg.Gateway.Port)
	fmt.Printf("Sidecar: %s\n", sc.URL())
	fmt.Printf("Token:   %s\n\n", cfg.Gateway.Token)

	if err := gw.Start(); err != nil && ctx.Err() == nil {
		log.Fatalf("Gateway error: %v", err)
	}
}

// cmdChat starts an interactive CLI chat session.
func cmdChat() {
	cfg, store := loadConfig()
	defer store.Close()
	credStore := loadCredentialStore(cfg)

	// Pick agent — first one, or specified via flag
	agentID := ""
	if len(os.Args) > 2 {
		agentID = os.Args[2]
	}

	var ac *config.AgentConfig
	for i := range cfg.Agents {
		if agentID == "" || cfg.Agents[i].ID == agentID {
			ac = &cfg.Agents[i]
			break
		}
	}
	if ac == nil {
		if agentID != "" {
			log.Fatalf("Agent %q not found in config", agentID)
		}
		log.Fatal("No agents configured. Run 'smithly init' first.")
	}

	a, err := loadAgent(*ac, cfg, store, credStore)
	if err != nil {
		log.Fatalf("Failed to load agent: %v", err)
	}

	cli := &channels.CLI{Agent: a}
	if err := cli.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// cmdAgent manages agents (list, add, remove).
func cmdAgent() {
	if len(os.Args) < 3 {
		fmt.Println(`Usage: smithly agent <subcommand>

Subcommands:
  list      List all configured agents
  add       Add a new agent (interactive)
  remove    Remove an agent by ID`)
		return
	}

	switch os.Args[2] {
	case "list":
		cmdAgentList()
	case "add":
		cmdAgentAdd()
	case "remove":
		cmdAgentRemove()
	default:
		fmt.Fprintf(os.Stderr, "unknown agent subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func cmdAgentList() {
	cfg, err := config.Load("smithly.toml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if len(cfg.Agents) == 0 {
		fmt.Println("No agents configured.")
		return
	}

	fmt.Printf("%-20s %-25s %-12s %s\n", "ID", "MODEL", "PROVIDER", "WORKSPACE")
	for _, a := range cfg.Agents {
		provider := a.Provider
		if provider == "" {
			provider = "openai"
		}
		toolInfo := ""
		if len(a.Tools) > 0 {
			toolInfo = fmt.Sprintf(" (tools: %s)", strings.Join(a.Tools, ", "))
		}
		fmt.Printf("%-20s %-25s %-12s %s%s\n", a.ID, a.Model, provider, a.Workspace, toolInfo)
	}
}

func cmdAgentAdd() {
	dir, _ := os.Getwd()
	configPath := filepath.Join(dir, "smithly.toml")

	if _, err := os.Stat(configPath); err != nil {
		log.Fatal("smithly.toml not found. Run 'smithly init' first.")
	}

	// Check for duplicate IDs
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Agent name: ")
	agentName, _ := reader.ReadString('\n')
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		log.Fatal("Agent name is required")
	}

	for _, a := range cfg.Agents {
		if a.ID == agentName {
			log.Fatalf("Agent %q already exists", agentName)
		}
	}

	provider, baseURL, model, apiKey := promptLLMConfig(reader)

	wsPath := filepath.Join("workspaces", agentName)
	os.MkdirAll(filepath.Join(dir, wsPath), 0755)

	writeIfMissing(filepath.Join(dir, wsPath, "SOUL.md"),
		"You are a helpful, thoughtful AI assistant. You communicate clearly and concisely.")
	writeIfMissing(filepath.Join(dir, wsPath, "IDENTITY.toml"),
		fmt.Sprintf("name = %q\nemoji = \"🤖\"\n", agentName))

	agentCfg := config.AgentConfig{
		ID:        agentName,
		Model:     model,
		Workspace: wsPath,
		Provider:  provider,
		APIKey:    apiKey,
		BaseURL:   baseURL,
	}

	if err := config.AppendAgent(configPath, agentCfg); err != nil {
		log.Fatalf("Failed to add agent: %v", err)
	}

	fmt.Printf("\nAgent %q added. Chat with: smithly chat %s\n", agentName, agentName)
}

func cmdAgentRemove() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: smithly agent remove <agent-id>")
		return
	}

	agentID := os.Args[3]
	configPath := "smithly.toml"

	if err := config.RemoveAgent(configPath, agentID); err != nil {
		log.Fatalf("Failed to remove agent: %v", err)
	}

	fmt.Printf("Agent %q removed from config.\n", agentID)
	fmt.Printf("Note: workspace directory was not deleted. Remove manually if desired.\n")
}

// cmdSkill manages instruction skills (list, add, remove).
func cmdSkill() {
	if len(os.Args) < 3 {
		fmt.Println(`Usage: smithly skill <subcommand>

Subcommands:
  list                List installed skills
  add <path>          Install a skill from a directory
  remove <name>       Remove an installed skill

Flags:
  --agent <id>        Target a specific agent (default: first agent)`)
		return
	}

	switch os.Args[2] {
	case "list":
		cmdSkillList()
	case "add":
		cmdSkillAdd()
	case "remove":
		cmdSkillRemove()
	default:
		fmt.Fprintf(os.Stderr, "unknown skill subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func cmdSkillList() {
	cfg, agentID := parseSkillFlags(3)
	ac := findAgent(cfg, agentID)

	skillsDir := filepath.Join(ac.Workspace, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		fmt.Println("No skills installed.")
		return
	}

	count := 0
	fmt.Printf("%-20s %-10s %s\n", "NAME", "VERSION", "DESCRIPTION")
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		s, err := skills.Load(filepath.Join(skillsDir, entry.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %s: %v\n", entry.Name(), err)
			continue
		}
		desc := s.Manifest.Skill.Description
		if desc == "" {
			desc = "(no description)"
		}
		version := s.Manifest.Skill.Version
		if version == "" {
			version = "-"
		}
		fmt.Printf("%-20s %-10s %s\n", s.Manifest.Skill.Name, version, desc)
		count++
	}
	if count == 0 {
		fmt.Println("No skills installed.")
	}
}

func cmdSkillAdd() {
	cfg, agentID := parseSkillFlags(4)
	ac := findAgent(cfg, agentID)

	// Find the source path argument (skip --agent flags)
	srcPath := ""
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--agent" {
			i++ // skip value
			continue
		}
		srcPath = args[i]
		break
	}
	if srcPath == "" {
		log.Fatal("Usage: smithly skill add <path> [--agent <id>]")
	}

	// Validate it's a loadable skill
	s, err := skills.Load(srcPath)
	if err != nil {
		log.Fatalf("Invalid skill at %s: %v", srcPath, err)
	}

	// Copy to workspace/skills/<name>/
	destDir := filepath.Join(ac.Workspace, "skills", s.Manifest.Skill.Name)
	if _, err := os.Stat(destDir); err == nil {
		log.Fatalf("Skill %q already installed. Remove it first with: smithly skill remove %s",
			s.Manifest.Skill.Name, s.Manifest.Skill.Name)
	}

	if err := copyDir(srcPath, destDir); err != nil {
		log.Fatalf("Failed to install skill: %v", err)
	}

	fmt.Printf("Installed skill %q into %s\n", s.Manifest.Skill.Name, destDir)

	// Warn about OAuth2 requirements
	if s.Manifest.Requires != nil && len(s.Manifest.Requires.OAuth2) > 0 {
		cfg, err := config.Load("smithly.toml")
		if err == nil {
			configured := make(map[string]bool)
			for _, p := range cfg.OAuth2 {
				configured[p.Name] = true
			}
			for _, provider := range s.Manifest.Requires.OAuth2 {
				if !configured[provider] {
					fmt.Printf("\n  Warning: skill requires OAuth2 provider %q which is not configured.\n", provider)
					fmt.Printf("  Add a [[oauth2]] section to smithly.toml, then run: smithly oauth2 auth %s\n", provider)
				}
			}
		}
	}
}

func cmdSkillRemove() {
	cfg, agentID := parseSkillFlags(4)
	ac := findAgent(cfg, agentID)

	// Find the skill name argument (skip --agent flags)
	skillName := ""
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--agent" {
			i++ // skip value
			continue
		}
		skillName = args[i]
		break
	}
	if skillName == "" {
		log.Fatal("Usage: smithly skill remove <name> [--agent <id>]")
	}

	destDir := filepath.Join(ac.Workspace, "skills", skillName)
	if _, err := os.Stat(destDir); err != nil {
		log.Fatalf("Skill %q not found in %s", skillName, filepath.Join(ac.Workspace, "skills"))
	}

	if err := os.RemoveAll(destDir); err != nil {
		log.Fatalf("Failed to remove skill: %v", err)
	}

	fmt.Printf("Removed skill %q\n", skillName)
}

// parseSkillFlags extracts --agent flag from args starting at position minArgs.
func parseSkillFlags(minArgs int) (*config.Config, string) {
	cfg, err := config.Load("smithly.toml")
	if err != nil {
		log.Fatalf("Failed to load config: %v\nRun 'smithly init' first.", err)
	}

	agentID := ""
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--agent" && i+1 < len(args) {
			agentID = args[i+1]
			break
		}
	}
	return cfg, agentID
}

// findAgent looks up an agent config by ID, or returns the first agent.
func findAgent(cfg *config.Config, agentID string) *config.AgentConfig {
	for i := range cfg.Agents {
		if agentID == "" || cfg.Agents[i].ID == agentID {
			return &cfg.Agents[i]
		}
	}
	if agentID != "" {
		log.Fatalf("Agent %q not found in config", agentID)
	}
	log.Fatal("No agents configured. Run 'smithly init' first.")
	return nil
}

// copyDir recursively copies a directory.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

// promptLLMConfig runs the interactive LLM provider/model/key prompts.
func promptLLMConfig(reader *bufio.Reader) (provider, baseURL, model, apiKey string) {
	fmt.Println("\nLLM Provider:")
	fmt.Println("  1. OpenAI")
	fmt.Println("  2. Anthropic (via OpenAI-compatible)")
	fmt.Println("  3. OpenRouter")
	fmt.Println("  4. Ollama (local)")
	fmt.Print("Choice [1]: ")
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)

	switch choice {
	case "2":
		baseURL = "https://api.anthropic.com/v1"
		provider = "anthropic"
	case "3":
		baseURL = "https://openrouter.ai/api/v1"
		provider = "openrouter"
	case "4":
		baseURL = "http://localhost:11434/v1"
		provider = "ollama"
	default:
		baseURL = "https://api.openai.com/v1"
		provider = "openai"
	}

	var defaultModel string
	switch provider {
	case "anthropic":
		defaultModel = "claude-sonnet-4-6-20250514"
	case "ollama":
		defaultModel = "llama3.2"
	default:
		defaultModel = "gpt-4o"
	}
	fmt.Printf("\nModel [%s]: ", defaultModel)
	model, _ = reader.ReadString('\n')
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultModel
	}

	if provider != "ollama" {
		fmt.Print("\nAPI key: ")
		apiKey, _ = reader.ReadString('\n')
		apiKey = strings.TrimSpace(apiKey)
	}
	return
}

// cmdOAuth2 manages OAuth2 providers (auth, list).
func cmdOAuth2() {
	if len(os.Args) < 3 {
		fmt.Println(`Usage: smithly oauth2 <subcommand>

Subcommands:
  auth <provider>   Authorize an OAuth2 provider (opens browser)
  list              List configured providers and auth status`)
		return
	}

	switch os.Args[2] {
	case "auth":
		cmdOAuth2Auth()
	case "list":
		cmdOAuth2List()
	default:
		fmt.Fprintf(os.Stderr, "unknown oauth2 subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func cmdOAuth2Auth() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: smithly oauth2 auth <provider>")
		return
	}
	providerName := os.Args[3]

	cfg, err := config.Load("smithly.toml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Find the OAuth2 provider config
	var providerCfg *config.OAuth2Config
	for i := range cfg.OAuth2 {
		if cfg.OAuth2[i].Name == providerName {
			providerCfg = &cfg.OAuth2[i]
			break
		}
	}
	if providerCfg == nil {
		fmt.Fprintf(os.Stderr, "OAuth2 provider %q not found in smithly.toml\n", providerName)
		fmt.Fprintf(os.Stderr, "\nConfigured providers:\n")
		for _, p := range cfg.OAuth2 {
			fmt.Fprintf(os.Stderr, "  - %s\n", p.Name)
		}
		os.Exit(1)
	}

	credStore := loadCredentialStore(cfg)

	// Start local callback server
	callbackPort := 18790
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no authorization code in callback")
			fmt.Fprintf(w, "Error: no authorization code received.")
			return
		}
		codeCh <- code
		fmt.Fprintf(w, "Authorization successful! You can close this tab.")
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", callbackPort),
		Handler: mux,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Build auth URL
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", callbackPort)
	authURL := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&access_type=offline&prompt=consent",
		providerCfg.AuthURL,
		providerCfg.ClientID,
		redirectURI,
		strings.Join(providerCfg.Scopes, " "),
	)

	fmt.Printf("Opening browser for %s authorization...\n", providerName)
	fmt.Printf("\nIf the browser doesn't open, visit:\n%s\n\n", authURL)

	// Try to open browser
	openBrowser(authURL)

	fmt.Println("Waiting for authorization callback...")

	// Wait for callback
	select {
	case code := <-codeCh:
		// Exchange code for tokens
		data := fmt.Sprintf("grant_type=authorization_code&code=%s&redirect_uri=%s&client_id=%s&client_secret=%s",
			code, redirectURI, providerCfg.ClientID, providerCfg.ClientSecret)

		resp, err := http.Post(providerCfg.TokenURL, "application/x-www-form-urlencoded", strings.NewReader(data))
		if err != nil {
			log.Fatalf("Token exchange failed: %v", err)
		}
		defer resp.Body.Close()

		var tokenResp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			TokenType    string `json:"token_type"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			log.Fatalf("Failed to parse token response: %v", err)
		}

		if resp.StatusCode != 200 {
			log.Fatalf("Token endpoint returned HTTP %d", resp.StatusCode)
		}

		token := &credentials.OAuth2Token{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			TokenType:    tokenResp.TokenType,
		}
		if tokenResp.ExpiresIn > 0 {
			token.Expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		}

		if err := credStore.Put(context.Background(), providerName, token); err != nil {
			log.Fatalf("Failed to save credentials: %v", err)
		}

		fmt.Printf("\n%s authorized successfully! Token saved.\n", providerName)

	case err := <-errCh:
		log.Fatalf("Authorization failed: %v", err)
	}

	srv.Shutdown(context.Background())
}

func cmdOAuth2List() {
	cfg, err := config.Load("smithly.toml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if len(cfg.OAuth2) == 0 {
		fmt.Println("No OAuth2 providers configured.")
		fmt.Println("\nAdd providers to smithly.toml:")
		fmt.Println("  [[oauth2]]")
		fmt.Println("  name = \"google\"")
		fmt.Println("  client_id = \"...\"")
		fmt.Println("  client_secret = \"...\"")
		fmt.Println("  scopes = [\"https://www.googleapis.com/auth/gmail.readonly\"]")
		fmt.Println("  auth_url = \"https://accounts.google.com/o/oauth2/auth\"")
		fmt.Println("  token_url = \"https://oauth2.googleapis.com/token\"")
		return
	}

	credStore := loadCredentialStore(cfg)

	fmt.Printf("%-20s %-12s %s\n", "PROVIDER", "STATUS", "SCOPES")
	for _, p := range cfg.OAuth2 {
		status := "not authorized"
		tok, err := credStore.Get(context.Background(), p.Name)
		if err == nil && tok != nil {
			if tok.RefreshToken != "" {
				status = "authorized"
			} else {
				status = "no refresh token"
			}
		}
		scopes := strings.Join(p.Scopes, ", ")
		if len(scopes) > 50 {
			scopes = scopes[:50] + "..."
		}
		fmt.Printf("%-20s %-12s %s\n", p.Name, status, scopes)
	}
}

func openBrowser(url string) {
	// Try common browser openers
	for _, cmd := range []string{"xdg-open", "open", "wslview"} {
		if _, err := exec.LookPath(cmd); err == nil {
			exec.Command(cmd, url).Start()
			return
		}
	}
}

// cmdAudit shows the audit log.
func cmdAudit() {
	_, store := loadConfig()
	defer store.Close()

	query := db.AuditQuery{Limit: 50}

	// Parse flags: smithly audit [--agent ID] [--limit N]
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 < len(args) {
				i++
				query.AgentID = args[i]
			}
		case "--limit":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil {
					query.Limit = n
				}
			}
		}
	}

	entries, err := store.GetAuditLog(context.Background(), query)
	if err != nil {
		log.Fatalf("Failed to read audit log: %v", err)
	}

	if len(entries) == 0 {
		fmt.Println("No audit entries found.")
		return
	}

	for _, e := range entries {
		target := ""
		if e.Target != "" {
			target = " → " + e.Target
		}
		details := ""
		if e.Details != "" {
			d := e.Details
			if len(d) > 80 {
				d = d[:80] + "..."
			}
			details = "  " + d
		}
		fmt.Printf("%s  %-20s  %-12s%s%s\n",
			e.Timestamp.Format("2006-01-02 15:04:05"),
			e.Actor,
			e.Action,
			target,
			details,
		)
	}
}

// cmdDoctor checks that dependencies are available.
func cmdDoctor() {
	fmt.Println("Smithly Doctor")
	fmt.Println()

	// Check for smithly.toml
	if _, err := os.Stat("smithly.toml"); err == nil {
		fmt.Println("  ✓ smithly.toml found")
	} else {
		fmt.Println("  ✗ smithly.toml not found (run 'smithly init')")
	}

	// Check for Docker
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		fmt.Println("  ✓ Docker socket found")
	} else {
		fmt.Println("  - Docker not available (optional, needed for sandbox)")
	}

	fmt.Println()
}

// --- helpers ---

func loadConfig() (*config.Config, db.Store) {
	cfg, err := config.Load("smithly.toml")
	if err != nil {
		log.Fatalf("Failed to load config: %v\nRun 'smithly init' to create one.", err)
	}

	store, err := sqlite.New(cfg.Storage.Database)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	if err := store.Migrate(context.Background()); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	return cfg, store
}

func loadCredentialStore(cfg *config.Config) credentials.Store {
	path := cfg.Credentials.Path
	if path == "" {
		path = "credentials.json"
	}
	return credentials.NewFileStore(path)
}

func loadAgent(ac config.AgentConfig, cfg *config.Config, store db.Store, credStore credentials.Store) (*agent.Agent, error) {
	ws, err := workspace.Load(ac.Workspace)
	if err != nil {
		return nil, fmt.Errorf("load workspace for %s: %w", ac.ID, err)
	}

	a := agent.New(ac.ID, ac.Model, ac.BaseURL, ac.APIKey, ws, store)
	a.MaxContext = ac.MaxContext

	// Configure cost-based spending limits
	a.Pricing = agent.LookupPricing(ac.Model)
	if ac.Pricing != nil {
		a.Pricing = agent.ModelPricing{
			InputPerMillion:       ac.Pricing.InputPerMillion,
			OutputPerMillion:      ac.Pricing.OutputPerMillion,
			CachedInputPerMillion: ac.Pricing.CachedPerMillion,
		}
	}
	var costConfigs []agent.CostLimitConfig
	for _, cl := range ac.CostLimits {
		costConfigs = append(costConfigs, agent.CostLimitConfig{
			Dollars: cl.Dollars,
			Window:  cl.Window,
		})
	}
	a.CostWindows = agent.ParseCostWindows(costConfigs)

	// Load instruction skills from workspace skills/ directory
	skillRegistry := skills.NewRegistry()
	skillsDir := filepath.Join(ac.Workspace, "skills")
	if entries, err := os.ReadDir(skillsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			s, err := skills.Load(filepath.Join(skillsDir, entry.Name()))
			if err != nil {
				log.Printf("warning: failed to load skill %s: %v", entry.Name(), err)
				continue
			}
			if err := skillRegistry.Add(s); err != nil {
				log.Printf("warning: skill %s: %v", entry.Name(), err)
				continue
			}
			log.Printf("loaded skill: %s", s.Manifest.Skill.Name)
		}
	}
	a.Skills = skillRegistry

	// Register built-in tools (filtered by agent's tool config)
	registerTools(a.Tools, cfg, ac.Tools, skillRegistry, credStore)

	// Ensure agent exists in DB
	if _, err := store.GetAgent(context.Background(), ac.ID); err != nil {
		store.CreateAgent(context.Background(), &db.Agent{
			ID:            ac.ID,
			Model:         ac.Model,
			WorkspacePath: ac.Workspace,
		})
	}

	return a, nil
}

func registerTools(registry *tools.Registry, cfg *config.Config, allowedTools []string, skillRegistry *skills.Registry, credStore credentials.Store) {
	// Build allowed set (empty = all allowed)
	allowed := make(map[string]bool)
	for _, t := range allowedTools {
		allowed[t] = true
	}
	isAllowed := func(name string) bool {
		return len(allowed) == 0 || allowed[name]
	}

	// Pick search provider based on config
	var searchProvider tools.SearchProvider
	switch cfg.Search.Provider {
	case "duckduckgo":
		searchProvider = tools.NewDuckDuckGoSearch()
	default: // "brave" or empty
		apiKey := cfg.Search.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("BRAVE_API_KEY")
		}
		if apiKey != "" {
			searchProvider = tools.NewBraveSearch(apiKey)
		} else {
			// Fall back to DuckDuckGo if no Brave key
			log.Println("warning: no BRAVE_API_KEY set, falling back to DuckDuckGo (limited results)")
			searchProvider = tools.NewDuckDuckGoSearch()
		}
	}

	// Build OAuth2 tool from config
	var oauth2Tool *tools.OAuth2Tool
	if len(cfg.OAuth2) > 0 && credStore != nil {
		oauth2Tool = tools.NewOAuth2Tool(cfg.OAuth2, credStore)
	}

	allTools := []tools.Tool{
		tools.NewSearchWithProvider(searchProvider),
		tools.NewFetch(),
		tools.NewBash(),
		tools.NewReadFile(""),
		tools.NewWriteFile(""),
		tools.NewListFiles(""),
		tools.NewClaudeCode(),
	}

	// Add OAuth2 + API call tools if OAuth2 providers are configured
	if oauth2Tool != nil {
		allTools = append(allTools, oauth2Tool)
		allTools = append(allTools, tools.NewAPICall(oauth2Tool))
	}

	// Add notify tool if configured
	if cfg.Notify.NtfyTopic != "" {
		provider := tools.NewNtfyProvider(cfg.Notify.NtfyTopic, cfg.Notify.NtfyServer)
		allTools = append(allTools, tools.NewNotify(provider))
	}

	// Add read_skill tool if there are skills installed
	if skillRegistry != nil && len(skillRegistry.All()) > 0 {
		allTools = append(allTools, tools.NewReadSkill(skillRegistry))
	}
	for _, t := range allTools {
		if isAllowed(t.Name()) {
			registry.Register(t)
		}
	}
}

func writeIfMissing(path, content string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	os.WriteFile(path, []byte(content+"\n"), 0644)
}

// startSidecar creates and starts the sidecar HTTP server in a goroutine.
func startSidecar(cfg *config.Config, dbStore db.Store, credStore credentials.Store) *sidecar.Sidecar {
	// Build OAuth2 tool for sidecar
	var oauth2Tool *tools.OAuth2Tool
	if len(cfg.OAuth2) > 0 && credStore != nil {
		oauth2Tool = tools.NewOAuth2Tool(cfg.OAuth2, credStore)
	}

	// Build notify provider for sidecar
	var notifyProvider tools.NotifyProvider
	if cfg.Notify.NtfyTopic != "" {
		notifyProvider = tools.NewNtfyProvider(cfg.Notify.NtfyTopic, cfg.Notify.NtfyServer)
	}

	// Build object store — uses a separate SQLite file so direct-connecting
	// skills can't access the immutable store tables.
	var objStore store.Store
	storeDBPath := strings.TrimSuffix(cfg.Storage.Database, ".db") + "_store.db"
	storeDB, err := sqlite.New(storeDBPath)
	if err != nil {
		log.Printf("warning: could not open store DB %s: %v", storeDBPath, err)
	} else {
		if err := storeDB.Migrate(context.Background()); err != nil {
			log.Printf("warning: store DB migration failed: %v", err)
		}
		objStore = store.NewSQLite(storeDB.DB())
	}

	// Build secret store from config
	secrets := loadSecretStore(cfg)

	bind := cfg.Sidecar.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	port := cfg.Sidecar.Port
	if port == 0 {
		port = 18791
	}

	sc := sidecar.New(sidecar.Config{
		Bind:     bind,
		Port:     port,
		OAuth2:   oauth2Tool,
		Notify:   notifyProvider,
		Audit:    dbStore,
		ObjStore: objStore,
		Secrets:  secrets,
	})

	go func() {
		log.Printf("sidecar listening on %s", sc.URL())
		if err := sc.Start(); err != nil {
			log.Printf("sidecar error: %v", err)
		}
	}()

	return sc
}

// configSecretStore implements sidecar.SecretStore from config entries.
type configSecretStore struct {
	secrets map[string]string
}

func (s *configSecretStore) GetSecret(name string) (string, bool) {
	v, ok := s.secrets[name]
	return v, ok
}

func loadSecretStore(cfg *config.Config) sidecar.SecretStore {
	secrets := make(map[string]string, len(cfg.Secrets))
	for _, s := range cfg.Secrets {
		if s.Env != "" {
			// Read from controller's environment — skill never sees the env var
			secrets[s.Name] = os.Getenv(s.Env)
		} else {
			secrets[s.Name] = s.Value
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	return &configSecretStore{secrets: secrets}
}
