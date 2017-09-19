package main

import (
	"os"
	"log"
	"github.com/proshik/githubstatbot/github"
	"github.com/proshik/githubstatbot/telegram"
	"github.com/julienschmidt/httprouter"
	"net/http"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

type Handler struct {
	ClientId     string
	ClientSecret string
	bot          *telegram.Bot
}

type AccessTokenReq struct {
	ClientId     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Code         string `json:"code"`
}

type AccessTokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		log.Panic("Port is empty")
	}

	clientId := os.Getenv("GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("GITHUB_CLIENT_SECRET")
	if clientId == "" || clientSecret == "" {
		log.Panic("ClientId or clientSecret is empty")
	}

	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	if telegramToken == "" {
		log.Panic("Telegram token is empty")
	}

	gitHubToken := os.Getenv("GITHUB_TOKEN")
	if gitHubToken == "" {
		log.Panic("GitHub token is empty")
	}

	ghClient, err := github.NewClient(gitHubToken)
	if err != nil {
		log.Panic(err)
	}

	bot, err := telegram.NewBot(telegramToken, false, clientId, clientSecret, ghClient)
	if err != nil {
		log.Panic(err)
	}

	go bot.ReadUpdates()

	h := &Handler{ClientId: clientId, ClientSecret: clientSecret, bot: bot}

	router := httprouter.New()
	router.GET("/github_redirect", h.GitHubAuth)

	log.Println("Service is waiting for requests...")

	err = http.ListenAndServe(":"+port, router)
	if err != nil {
		log.Fatal(err)
	}
}

func (h *Handler) GitHubAuth(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		log.Printf("Error on received response with code from GitHub.com. Code is empty.")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	bodyReq := AccessTokenReq{h.ClientId, h.ClientSecret, code}
	b := new(bytes.Buffer)
	json.NewEncoder(b).Encode(bodyReq)

	req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", b)
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Erorr on build request object. Error: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	defer resp.Body.Close()

	var bodyResp AccessTokenResp
	json.NewDecoder(resp.Body).Decode(&bodyResp)

	fmt.Printf("Received access_token=%s\n", bodyResp.AccessToken)

	chatId, err := strconv.Atoi(state)
	if err != nil {
		log.Printf("Error on convert code=%s to chatId\n", code)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	h.bot.Storage.Add(int64(chatId), bodyResp.AccessToken)

	http.Redirect(w, r, "https://t.me/GitHubStatBot", http.StatusMovedPermanently)
}
