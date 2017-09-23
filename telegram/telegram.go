package telegram

import (
	"gopkg.in/telegram-bot-api.v4"
	"log"
	"bytes"
	"fmt"
	"sync"
	"sort"
	"github.com/proshik/githubstatbot/github"
	"math/rand"
)

var (
	//Chains
	//-commands
	startC    = make(chan tgbotapi.Update)
	authC     = make(chan tgbotapi.Update)
	languageC = make(chan tgbotapi.Update)
	cancelC   = make(chan tgbotapi.Update)
	//-send message
	messages = make(chan tgbotapi.Chattable)

	//Randomize
	letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
)

type Language struct {
	Title      string
	Percentage float32
}

func (b *Bot) ReadUpdates() {
	//create timeout value
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	//read updates from telegram server
	updates, err := b.bot.GetUpdatesChan(u)
	if err != nil {
		log.Panic(err)
	}
	//handle commands from channels
	go func() {
		for {
			select {
			case u := <-startC:
				messages <- startCommand(&u)
			case u := <-authC:
				messages <- authCommand(&u, b)
			case u := <-languageC:
				messages <- languageCommand(&u, b)
			case u := <-cancelC:
				messages <- cancelCommand(&u, b)
			}
		}
	}()
	//Отправка сообщений пользователям.
	//Отдельно от предыдущего блока т.к. нельзя в select нельзя обрабатывать каналы команд из которох читается(*С)
	//и куда записыватеся(messages)
	go func() {
		for res := range messages {
			b.bot.Send(res)
		}
	}()
	//read updates and send to channels
	for update := range updates {
		if update.Message.IsCommand() {
			switch update.Message.Command() {
			case "start":
				startC <- update
			case "auth":
				authC <- update
			case "language":
				languageC <- update
			case "cancel":
				cancelC <- update
			default:
				//show access commands
				startC <- update
			}
		} else {
			startC <- update
		}
	}
}

func (b *Bot) InformAuth(chatId int64, result bool) {
	if result {
		messages <- tgbotapi.NewMessage(chatId, "GitHub аккаунт был успешно подключен!")
	} else {
		messages <- tgbotapi.NewMessage(chatId, "Произошла ошибка при подключении GitHub аккаунта!")
	}
}

func startCommand(update *tgbotapi.Update) tgbotapi.MessageConfig {
	buf := bytes.NewBufferString("Телеграм бот для отображения статистики GitHub аккаунта\n")
	//descriptions of commands
	buf.WriteString("\n")
	buf.WriteString("Вы можете управлять мной, отправляя следующие команды:\n\n")
	buf.WriteString("*/auth* - авторизация через OAuth\n")
	//buf.WriteString("*/stat* - статистика по репозиториям\n")
	//buf.WriteString("*/stat <username>* - статистика по заданному пользователю\n")
	buf.WriteString("*/language* - статистика используемых языков в репозиториях\n")
	buf.WriteString("*/cancel* - отмена авторизации\n")
	//create message
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, buf.String())
	msg.ParseMode = "markdown"
	return msg
}

func authCommand(update *tgbotapi.Update, bot *Bot) tgbotapi.Chattable {
	//check, maybe user already authorize
	token := bot.tokenStore.Get(update.Message.Chat.ID)
	if token != "" {
		return tgbotapi.NewMessage(update.Message.Chat.ID, "Вы уже авторизованы!")
	}
	//generate state for url string for auth in github
	state := randStringRunes(20)
	//save to store [state]chatId
	bot.stateStore.Add(state, update.Message.Chat.ID)
	//build url
	authUrl := bot.oAuth.BuildAuthUrl(state)
	//build text for message
	buf := bytes.NewBufferString("Для авторизации перейдите по следующей ссылке:\n")
	buf.WriteString("\n")
	buf.WriteString(authUrl + "\n")
	//build message with url for user
	text := buf.String()
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	return msg
}

func languageCommand(update *tgbotapi.Update, bot *Bot) tgbotapi.MessageConfig {
	//found token by chatId in store
	token := bot.tokenStore.Get(update.Message.Chat.ID)
	if token == "" {
		return tgbotapi.NewMessage(update.Message.Chat.ID, "Необходимо выполнить авторизацию. Команда /auth")
	}
	//create github client
	client := github.NewClient(token)

	//receipt info for user
	username, err := client.User()
	if err != nil {
		return tgbotapi.NewMessage(update.Message.Chat.ID, "Ошибка получения данных. Выполните повторную авторизацию")
	}
	//receipt user repositories
	repos, err := client.Repos(username)
	if err != nil {
		return tgbotapi.NewMessage(update.Message.Chat.ID, "Not found repos for username="+username)
	}
	//concurrent receipt language info in repositories of an user
	wg := sync.WaitGroup{}
	languageChan := make(chan map[string]int)
	for _, repo := range repos {
		wg.Add(1)
		go func(wg *sync.WaitGroup, r *github.Repo) {
			defer wg.Done()
			//receipt language info
			lang, err := client.Language(username, *r.Name)
			if err != nil {
				log.Printf("Error on request language for user=%s, repo=%s", username, *r.Name)
			}
			languageChan <- lang
		}(&wg, repo)

	}
	//wait before not will be receipt language info
	go func() {
		wg.Wait()
		close(languageChan)
	}()
	//calculate sum of a bytes in each repository by language name
	statistics := make(map[string]int)
	for stat := range languageChan {
		for k, v := range stat {
			statistics[k] = statistics[k] + v
		}
	}
	//create text messages for user
	percentages := calcPercentages(statistics)
	text := createLangStatText(username, percentages)
	//create messages
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	msg.ParseMode = "markdown"
	return msg
}

func cancelCommand(update *tgbotapi.Update, bot *Bot) tgbotapi.Chattable {
	//check on exists token in store
	token := bot.tokenStore.Get(update.Message.Chat.ID)
	if token == "" {
		return tgbotapi.NewMessage(update.Message.Chat.ID, "Вы не авторизованы!")
	}
	//delete token by chatId. Exactly remove user from store
	bot.tokenStore.Delete(update.Message.Chat.ID)
	return tgbotapi.NewMessage(update.Message.Chat.ID, "GitHub аккаунт отключен!")
}

func calcPercentages(languages map[string]int) []*Language {
	result := make([]*Language, 0)
	//calculate total sum byte by all languages
	var totalSum float32
	for _, v := range languages {
		totalSum += float32(v)
	}

	var totalByteOtherLanguages int
	for key, value := range languages {
		repoPercent := float32(value) * (float32(100) / totalSum)
		roundRepoPercent := round(repoPercent, 0.1)
		if roundRepoPercent >= 0.1 {
			result = append(result, &Language{key, roundRepoPercent})
		} else {
			totalByteOtherLanguages += value
		}
	}
	//sort found languages by percentage
	sort.Slice(result, func(i, j int) bool { return result[i].Percentage > result[j].Percentage })
	//calculate percentage for language with less then 0.1% from total count
	if totalByteOtherLanguages != 0 {
		percent := round(float32(totalByteOtherLanguages)*(float32(100)/totalSum), 0.1)
		result = append(result, &Language{"Other languages", percent})
	}
	return result
}

func round(x, unit float32) float32 {
	if x > 0 {
		return float32(int32(x/unit+0.5)) * unit
	}
	return float32(int32(x/unit-0.5)) * unit
}

func createLangStatText(username string, statistics []*Language) string {
	buf := bytes.NewBufferString("")

	for _, l := range statistics {
		buf.WriteString(fmt.Sprintf("*%s* %.1f%%\n", l.Title, l.Percentage))
	}
	return buf.String()
}

func randStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}
