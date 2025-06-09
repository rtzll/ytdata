package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const (
	version             = "0.2.0"
	credentialsFile     = "youtube_credentials.json"
	clientSecretsPrefix = "client_secret_"
	clientSecretsSuffix = ".apps.googleusercontent.com.json"
)

var (
	scopes = []string{youtube.YoutubeReadonlyScope}
)

type Config struct {
	ClientSecret string
	Credentials  string
	OutputFile   string
}

func getConfigDir() string {
	if configDir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(configDir, "ytdata")
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(homeDir, ".ytdata")
	}
	return "."
}

func getDefaultCredentialsPath() string {
	return filepath.Join(getConfigDir(), credentialsFile)
}

func saveCredentials(path string, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	tokenData, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("failed to serialize token: %w", err)
	}

	if err := os.WriteFile(path, tokenData, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}
	return nil
}

func main() {
	var config Config

	rootCmd := &cobra.Command{
		Use:           "ytdata",
		Short:         "YouTube data export tool",
		Long:          "A CLI tool to export YouTube data including liked videos, subscriptions, and playlists",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.PersistentFlags().StringVarP(&config.ClientSecret, "client-secret", "s", "", "Path to client secrets JSON file (auto-detected if not specified)")
	rootCmd.PersistentFlags().StringVarP(&config.Credentials, "credentials", "c", getDefaultCredentialsPath(), "Path to credentials JSON file")

	cobra.OnInitialize(func() {
		if config.ClientSecret == "" {
			if v := os.Getenv("YTDATA_CLIENT_SECRET"); v != "" {
				config.ClientSecret = v
			}
		}
		if v := os.Getenv("YTDATA_CREDENTIALS"); v != "" {
			config.Credentials = v
		}
	})

	rootCmd.PersistentFlags().BoolP("version", "v", false, "Show version")
	cobra.CheckErr(rootCmd.RegisterFlagCompletionFunc("version", nil))

	setupCmd := &cobra.Command{
		Use:     "init",
		Aliases: []string{"setup"},
		Short:   "Interactive setup for YouTube API credentials",
		Long:    "Guide you through setting up Google Cloud project and OAuth2 credentials",
		Args:    cobra.NoArgs,
		Example: "  ytdata init",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup()
		},
	}

	likedCmd := &cobra.Command{
		Use:          "liked",
		Short:        "Fetch liked videos",
		Long:         "Fetch all liked videos and export to JSONL format",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example: `  ytdata liked
  ytdata liked -o liked.jsonl
  ytdata liked | jq .`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return createCommandHandler(cmd, &config, fetchLikedVideos)
		},
	}

	subscriptionsCmd := &cobra.Command{
		Use:          "subscriptions",
		Short:        "Fetch subscriptions",
		Long:         "Fetch all subscriptions and export to JSONL format",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example: `  ytdata subscriptions
  ytdata subscriptions -o subscriptions.jsonl`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return createCommandHandler(cmd, &config, fetchSubscriptions)
		},
	}

	playlistsCmd := &cobra.Command{
		Use:          "playlists",
		Short:        "Fetch playlists",
		Long:         "Fetch all user created playlists and export to JSONL format",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example: `  ytdata playlists
  ytdata playlists -o playlists.jsonl`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return createCommandHandler(cmd, &config, fetchPlaylists)
		},
	}

	addOutputFlag(likedCmd, "", "Write liked videos to stdout (or file with -o)")
	addOutputFlag(subscriptionsCmd, "", "Write subscriptions to stdout (or file with -o)")
	addOutputFlag(playlistsCmd, "", "Write playlists to stdout (or file with -o)")

	rootCmd.AddCommand(setupCmd, likedCmd, subscriptionsCmd, playlistsCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func authenticateYouTube(config Config) (*youtube.Service, error) {
	ctx := context.Background()

	oauthConfig, err := getOAuthConfig(config.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to get oauth config: %w", err)
	}

	// Load existing token if available
	var token *oauth2.Token
	if _, err := os.Stat(config.Credentials); err == nil {
		tokenData, err := os.ReadFile(config.Credentials)
		if err == nil {
			if err := json.Unmarshal(tokenData, &token); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to unmarshal token: %v\n", err)
			}
		}
	}

	// If we have any token (even expired), let OAuth2 client handle refresh
	if token != nil {
		// Create token source that will handle refresh automatically
		tokenSource := oauthConfig.TokenSource(ctx, token)

		// Get a fresh token (will refresh if needed)
		freshToken, err := tokenSource.Token()
		if err == nil {
			// Save the potentially refreshed token
			if err := saveCredentials(config.Credentials, freshToken); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to save refreshed credentials: %v\n", err)
			}

			// Create client with the fresh token
			client := oauthConfig.Client(ctx, freshToken)
			service, err := youtube.NewService(ctx, option.WithHTTPClient(client))
			if err == nil {
				return service, nil
			}
		}
	}

	// Only do full OAuth flow if no token or refresh failed
	token, err = performOAuthFlow(oauthConfig)
	if err != nil {
		return nil, fmt.Errorf("oauth flow failed: %w", err)
	}

	// Save new token
	if err := saveCredentials(config.Credentials, token); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to save credentials: %v\n", err)
	}

	client := oauthConfig.Client(ctx, token)
	service, err := youtube.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create youtube service: %w", err)
	}

	return service, nil
}

func getOAuthConfig(clientSecretsFile string) (*oauth2.Config, error) {
	b, err := os.ReadFile(clientSecretsFile)
	if err != nil {
		return nil, fmt.Errorf("unable to read client secret file: %w", err)
	}

	config, err := google.ConfigFromJSON(b, scopes...)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file: %w", err)
	}

	return config, nil
}

func performOAuthFlow(config *oauth2.Config) (*oauth2.Token, error) {
	codeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	// Start local server on port 8080 for OAuth callback
	config.RedirectURL = "http://localhost:8080/"

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("no authorization code received")
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
<title>Authorization Complete</title>
<meta charset="utf-8">
</head>
<body style="font-family: Arial, sans-serif; text-align: center; padding: 50px;">
<h2>Authorization Complete</h2>
<p>You can close this window and return to the terminal.</p>
</body>
</html>`); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to write response: %v\n", err)
		}

		codeChan <- code
	})

	server := &http.Server{Addr: ":8080"}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("failed to serve OAuth callback: %w", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Println("Opening authorization URL in browser...")

	if err := openBrowser(authURL); err != nil {
		fmt.Printf("Go to: %s\n", authURL)
	}

	var authCode string
	select {
	case code := <-codeChan:
		authCode = code
	case err := <-errChan:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("authorization timeout - please try again")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to shutdown server gracefully: %v\n", err)
	}

	token, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}

	return token, nil
}

func findClientSecretsFile() (string, error) {
	pattern := clientSecretsPrefix + "*" + clientSecretsSuffix

	// Search in multiple locations
	searchDirs := []string{
		".",                      // Current working directory
		getConfigDir(),           // User config directory
		filepath.Dir(os.Args[0]), // Binary directory
	}

	for _, dir := range searchDirs {
		fullPattern := filepath.Join(dir, pattern)
		files, err := filepath.Glob(fullPattern)
		if err != nil {
			// Log the error but continue searching in other directories
			fmt.Fprintf(os.Stderr, "Warning: Error searching in %s: %v\n", dir, err)
			continue
		}
		if len(files) > 0 {
			return files[0], nil
		}
	}

	return "", fmt.Errorf("no client secrets file found")
}

func ensureSetup(config *Config) error {
	if config.ClientSecret == "" {
		detected, err := findClientSecretsFile()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: no client secrets file found")
			fmt.Fprintln(os.Stderr, "Run 'ytdata init' for guided setup instructions")
			return fmt.Errorf("setup required: %w", err)
		}
		config.ClientSecret = detected
	}

	if _, err := os.Stat(config.ClientSecret); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: client secrets file not found at: %s\n", config.ClientSecret)
		fmt.Fprintln(os.Stderr, "Run 'ytdata init' for guided setup instructions")
		return fmt.Errorf("setup required: client secrets file not found")
	}

	if err := validateClientSecretsFile(config.ClientSecret); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid client secrets file: %v\n", err)
		fmt.Fprintln(os.Stderr, "Run 'ytdata init' for guided setup instructions")
		return fmt.Errorf("setup required: %w", err)
	}

	return nil
}

func validateClientSecretsFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read client secrets file: %w", err)
	}

	var secrets struct {
		Web struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"web"`
	}

	if err := json.Unmarshal(data, &secrets); err != nil {
		return fmt.Errorf("invalid JSON format: %w", err)
	}

	if secrets.Web.ClientID != "" && secrets.Web.ClientSecret != "" {
		return nil
	}

	return fmt.Errorf("must be web application type with valid client_id and client_secret")
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default:
		cmd = "xdg-open"
	}

	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}

func promptUser(message string) string {
	fmt.Print(message)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

func runSetup() error {
	fmt.Println("YouTube Data CLI — setup")
	fmt.Println()

	fmt.Println("This tool requires Google Cloud Project setup and OAuth2 credentials.")
	fmt.Println("I'll guide you through the process step by step.")
	fmt.Println()

	fmt.Println("Step 1: Google Cloud Project Setup")
	fmt.Println("You need a Google Cloud Project with YouTube Data API v3 enabled.")
	fmt.Println()

	if promptUser("Do you already have a Google Cloud Project? (y/N): ") != "y" {
		fmt.Println()
		fmt.Println("Creating a new Google Cloud Project:")
		fmt.Println("1. Go to: https://console.cloud.google.com/")
		fmt.Println("2. Click 'Select a project' dropdown")
		fmt.Println("3. Click 'New Project'")
		fmt.Println("4. Enter a project name (e.g., 'ytdata-cli')")
		fmt.Println("5. Click 'Create'")
		fmt.Println()

		promptUser("Press Enter when you've created the project... ")
	}

	fmt.Println()
	fmt.Println("Step 2: Enable YouTube Data API v3")
	fmt.Println("1. In your Google Cloud Project, go to 'APIs & Services' > 'Library'")
	fmt.Println("2. Search for 'YouTube Data API v3'")
	fmt.Println("3. Click on it and click 'Enable'")
	fmt.Println()

	fmt.Println("API URL: https://console.cloud.google.com/apis/library/youtube.googleapis.com")
	promptUser("Press Enter when you've enabled the API... ")

	fmt.Println()
	fmt.Println("Step 3: Create OAuth2 Credentials")
	fmt.Println("1. Go to 'APIs & Services' > 'Credentials'")
	fmt.Println("2. Click 'Create Credentials' > 'OAuth client ID'")
	fmt.Println("3. If prompted, configure the OAuth consent screen first:")
	fmt.Println("   - Choose 'External' user type")
	fmt.Println("   - Fill in required fields (app name, user support email)")
	fmt.Println("   - Add your email to test users")
	fmt.Println("4. For OAuth client ID:")
	fmt.Println("   - Application type: 'Web application'")
	fmt.Println("   - Name: 'YouTube Data CLI' (or any name)")
	fmt.Println("   - Add http://localhost:8080 to 'Authorized redirect URIs'")
	fmt.Println("5. Click 'Create'")
	fmt.Println("6. Download the JSON file")
	fmt.Println()

	fmt.Println("Credentials URL: https://console.cloud.google.com/apis/credentials")
	fmt.Println()
	fmt.Println("Step 4: Place the Credentials File")
	fmt.Println("1. Download the JSON file from step 3")
	fmt.Printf("2. Place it in your config directory: %s\n", getConfigDir())
	fmt.Println("   (or alternatively in the current directory)")
	fmt.Println("3. The filename should look like:")
	fmt.Println("   client_secret_XXXXX.apps.googleusercontent.com.json")
	fmt.Println()

	promptUser("Press Enter when you've placed the file... ")

	fmt.Println()
	fmt.Println("Verifying setup...")

	detected, err := findClientSecretsFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Println("Please ensure you've downloaded and placed the client secrets file correctly.")
		return err
	}

	fmt.Printf("Found client secrets file: %s\n", detected)

	if err := validateClientSecretsFile(detected); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Println("Please ensure you downloaded the correct OAuth2 client credentials.")
		return err
	}

	fmt.Println("Client secrets file is valid")
	fmt.Println()
	fmt.Println("Step 5: Test Authentication")
	fmt.Println("Let's verify everything works by completing the OAuth flow...")
	fmt.Println()

	// Test OAuth flow with the detected credentials
	config := Config{
		ClientSecret: detected,
		Credentials:  getDefaultCredentialsPath(),
	}

	_, err = authenticateYouTube(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Println("Please check your OAuth2 configuration and try again.")
		return err
	}

	fmt.Println("Authentication successful")
	fmt.Println()
	fmt.Println("Setup complete")
	fmt.Println("You can now use the following commands:")
	fmt.Println("  ytdata liked         # Fetch your liked videos (default: stdout)")
	fmt.Println("  ytdata liked -o FILE # Fetch your liked videos (write to FILE)")
	fmt.Println("  # similar for subscriptions and playlists")

	return nil
}

func fetchLikedVideos(config Config) error {
	service, err := authenticateYouTube(config)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	var allVideos []*youtube.Video
	pageToken := ""

	for {
		call := service.Videos.List([]string{"snippet", "contentDetails", "statistics"}).
			MyRating("like").
			MaxResults(50)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		response, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to fetch liked videos: %w", err)
		}

		allVideos = append(allVideos, response.Items...)

		if response.NextPageToken == "" {
			break
		}
		pageToken = response.NextPageToken
	}

	var writer io.Writer
	if config.OutputFile == "" {
		writer = os.Stdout
	} else {
		f, err := os.Create(config.OutputFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to close file: %v\n", err)
			}
		}()
		writer = f
	}
	encoder := json.NewEncoder(writer)
	for _, video := range allVideos {
		if err := encoder.Encode(video); err != nil {
			return fmt.Errorf("failed to write video data: %w", err)
		}
	}

	return nil
}

func fetchSubscriptions(config Config) error {
	service, err := authenticateYouTube(config)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	var subscriptions []*youtube.Subscription
	pageToken := ""

	for {
		call := service.Subscriptions.List([]string{"snippet"}).
			Mine(true).
			MaxResults(50)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		response, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to fetch subscriptions: %w", err)
		}

		subscriptions = append(subscriptions, response.Items...)

		if response.NextPageToken == "" {
			break
		}
		pageToken = response.NextPageToken
	}

	var channelIDs []string
	for _, sub := range subscriptions {
		channelIDs = append(channelIDs, sub.Snippet.ResourceId.ChannelId)
	}

	var allChannels []*youtube.Channel
	batchSize := 50

	for i := 0; i < len(channelIDs); i += batchSize {
		end := i + batchSize
		if end > len(channelIDs) {
			end = len(channelIDs)
		}

		batch := channelIDs[i:end]
		call := service.Channels.List([]string{
			"snippet", "contentDetails", "statistics", "topicDetails",
			"status", "brandingSettings", "localizations",
		}).Id(batch...)

		response, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to fetch channel details: %w", err)
		}

		allChannels = append(allChannels, response.Items...)
	}

	var writer io.Writer
	if config.OutputFile == "" {
		writer = os.Stdout
	} else {
		f, err := os.Create(config.OutputFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to close file: %v\n", err)
			}
		}()
		writer = f
	}
	encoder := json.NewEncoder(writer)
	for _, channel := range allChannels {
		if err := encoder.Encode(channel); err != nil {
			return fmt.Errorf("failed to write channel data: %w", err)
		}
	}

	return nil
}

// Helper function to add output flag with short option to commands
func addOutputFlag(cmd *cobra.Command, defaultValue, description string) {
	cmd.Flags().StringP("output", "o", defaultValue, description)
}

// Helper function to get output flag value and set it in config
func getOutputFlag(cmd *cobra.Command, config *Config) error {
	output, err := cmd.Flags().GetString("output")
	if err != nil {
		return fmt.Errorf("failed to get output flag: %w", err)
	}
	config.OutputFile = output
	return nil
}

// Common command handler that handles setup and flag parsing
func createCommandHandler(cmd *cobra.Command, config *Config, fetchFunc func(Config) error) error {
	if err := ensureSetup(config); err != nil {
		cmd.SilenceUsage = true
		return err
	}
	if err := getOutputFlag(cmd, config); err != nil {
		return err
	}
	return fetchFunc(*config)
}

func fetchPlaylists(config Config) error {
	service, err := authenticateYouTube(config)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Fetch user-created playlists only
	// Note: Special playlists (uploads, liked videos) could be fetched via:
	// service.Channels.List([]string{"contentDetails"}).Mine(true) -> RelatedPlaylists
	var allPlaylists []*youtube.Playlist
	pageToken := ""
	for {
		call := service.Playlists.List([]string{"snippet", "contentDetails", "status"}).
			Mine(true).MaxResults(50)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		response, err := call.Do()
		if err != nil {
			return fmt.Errorf("failed to fetch playlists: %w", err)
		}
		allPlaylists = append(allPlaylists, response.Items...)
		if response.NextPageToken == "" {
			break
		}
		pageToken = response.NextPageToken
	}

	var writer io.Writer
	if config.OutputFile == "" {
		writer = os.Stdout
	} else {
		f, err := os.Create(config.OutputFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to close file: %v\n", err)
			}
		}()
		writer = f
	}
	encoder := json.NewEncoder(writer)
	for _, playlist := range allPlaylists {
		if err := encoder.Encode(playlist); err != nil {
			return fmt.Errorf("failed to write playlist data: %w", err)
		}
	}

	return nil
}
