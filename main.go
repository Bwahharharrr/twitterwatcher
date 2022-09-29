package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"strconv"
	"strings"

	// "github.com/BurntSushi/toml"
	"github.com/himanshujaju/localdb"
	"github.com/k0kubun/pp/v3"
	"github.com/multiplay/go-ts3"

	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"

	"github.com/pelletier/go-toml/v2"
	cron "github.com/robfig/cron/v3"
)

type account struct {
	Name      string
	Rg        string
	RoomGroup roomgroup
	// IncReplies bool
}
type roomgroup struct {
	Name  string
	Rooms []int
}

type Config struct {
	CRON_SCHEDULE          string
	TEAMSPEAK_IP           string
	TEAMSPEAK_API_USER     string
	TEAMSPEAK_API_PASSWORD string
	TEAMSPEAK_SERVER_ID    int
	TEAMSPEAK_BOT_USERNAME string

	TWITTER_URL             string
	TWITTER_CONSUMER_KEY    string
	TWITTER_CONSUMER_SECRET string
	TWITTER_ACCESS_TOKEN    string
	TWITTER_ACCESS_SECRET   string

	// ROOMGROUPS [][]string
	ROOMGROUPS []roomgroup
	ACCOUNTS   []account
}

func getRoomGroup(rgs []roomgroup, name string) roomgroup {
	for _, rg := range rgs {
		if rg.Name == name {
			return rg
		}
	}
	return roomgroup{}
}
func genTwitterUserSearch(accs []account) (string, error) {
	if len(accs) == 0 {
		return "", errors.New("Missing accounts to query twitter")
	}
	str := strings.Builder{}
	str.WriteString("(")
	for i, a := range accs {
		str.WriteString(fmt.Sprintf("from:%s", a.Name))
		// If its not the last
		if i != len(accs)-1 {
			str.WriteString(", OR ")
		}
	}
	str.WriteString(")")
	return str.String(), nil
}

func writeTweet(cfg Config, t twitter.Tweet) string {
	out := strings.Builder{}
	out.WriteString("🐦")

	bbUser := fmt.Sprintf("[b][u]%s[/u][/b]", t.User.ScreenName)
	out.WriteString(bbUser)

	out.WriteString(" - ")
	out.WriteString(t.Text)
	out.WriteString(" - ")

	url := fmt.Sprintf(cfg.TWITTER_URL, t.User.ScreenName, t.ID)
	// bburl := fmt.Sprintf("[url=%s]link[/url]", url)
	bburl := fmt.Sprintf("[url=%s]%s[/url]", url, url)
	out.WriteString(bburl)

	return out.String()
}

func RunScript(cfg Config) {

	// Prepare the tweet container
	outTweets := make(map[int][]string, 0)
	outTweetsCnt := 0

	for _, rg := range cfg.ROOMGROUPS {
		for _, roomID := range rg.Rooms {
			outTweets[roomID] = []string{}
		}
	}

	// -----------------------------------------------------
	// -----------------------------------------------------
	// -----------------------------------------------------

	// Build the twitter User query
	twitterQuery, err := genTwitterUserSearch(cfg.ACCOUNTS)
	if err != nil {
		log.Fatal(err.Error())
	}
	// pp.Println(twitterQuery)

	// Get the twitter cleint
	config := oauth1.NewConfig(cfg.TWITTER_CONSUMER_KEY, cfg.TWITTER_CONSUMER_SECRET)
	token := oauth1.NewToken(cfg.TWITTER_ACCESS_TOKEN, cfg.TWITTER_ACCESS_SECRET)
	httpClient := config.Client(oauth1.NoContext, token)
	client := twitter.NewClient(httpClient)

	// Get the local database
	database := localdb.CreateDB(".lastTweets")

	// Query Twitter
	search, _, err := client.Search.Tweets(&twitter.SearchTweetParams{
		Query: twitterQuery,
	})
	if err != nil {
		log.Print(err)
	}

	// Go through the Search results backwards
	for i := len(search.Statuses) - 1; i >= 0; i-- {
		t := search.Statuses[i]

		// Disallow replies
		if t.InReplyToUserID != 0 {
			continue
		}

		lastTweetID_Str, err := database.Get(t.User.ScreenName)
		lastTweetId_Int, _ := strconv.Atoi(lastTweetID_Str)

		processTweet := false
		// Do we have a previous tweet on record
		// If not, then we can process this tweet
		if err != nil {
			processTweet = true
		}
		// Is the current tweet were looking at newer than the last on record
		// If so then we can process this tweet
		if t.ID > int64(lastTweetId_Int) {
			processTweet = true
		}

		// We can't process the tweet so move on
		if processTweet != true {
			continue
		}

		// Get the matching account
		var acc account
		for _, v := range cfg.ACCOUNTS {
			if v.Name == t.User.ScreenName {
				acc = v
			}
		}

		// Get the accounts rooms
		// Add this tweet to write to these rooms
		for _, roomId := range acc.RoomGroup.Rooms {
			outTweets[roomId] = append(outTweets[roomId], writeTweet(cfg, t))
			outTweetsCnt += 1
		}

		// Finally set the users last tweet on record to be the current tweet
		database.Set(t.User.ScreenName, strconv.FormatInt(t.ID, 10))
	}

	// If we have some tweets to post to TS then process them
	if outTweetsCnt > 0 {
		tsProcessMessages(cfg, outTweets)
	} else {
		fmt.Println("No tweets to process")
		// No tweets to process")
	}

}
func tsProcessMessages(cfg Config, outTweets map[int][]string) {

	// #############################################
	c, err := ts3.NewClient(cfg.TEAMSPEAK_IP)

	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	if err := c.Login(cfg.TEAMSPEAK_API_USER, cfg.TEAMSPEAK_API_PASSWORD); err != nil {
		log.Fatal(err)
	}

	if v, err := c.Version(); err != nil {
		log.Fatal(err)
	} else {
		log.Println("server is running:", v)
	}

	c.Use(cfg.TEAMSPEAK_SERVER_ID) // Main room
	c.SetNick(cfg.TEAMSPEAK_BOT_USERNAME)

	tsclient, err := c.Whoami()
	if err != nil {
		log.Fatal("err", err.Error())
	}

	for roomId, tweets := range outTweets {

		log.Println("Moving client to:", roomId)

		cmd := ts3.NewCmd("clientmove")
		cmd.WithArgs(
			ts3.NewArg("clid", tsclient.ClientID),
			ts3.NewArg("cid", roomId),
		)
		out, err := c.ExecCmd(cmd)
		_ = out
		if err != nil {
			log.Fatal("err", err.Error())
		}

		for _, tStr := range tweets {
			cmd = ts3.NewCmd("sendtextmessage")
			cmd.WithArgs(
				// Client is 1, Channel is 2, Server is 3
				ts3.NewArg("targetmode", 2),
				// ServerId, ChannelId, ClientId
				ts3.NewArg("target", roomId),
				ts3.NewArg("msg", tStr),
			)
			out1, err := c.ExecCmd(cmd)
			if err != nil {
				log.Fatal("err", err.Error())
			} else {
				pp.Println(fmt.Sprintf("Msged: %d - %s", roomId, tStr))
				_ = out1
			}
		}
	}

}

func main() {

	doc, err := ioutil.ReadFile("config.toml")
	if err != nil {
		log.Fatal(err)
	}

	var cfg Config
	err1 := toml.Unmarshal(doc, &cfg)
	if err != nil {
		log.Fatal(err1)
	}

	for i, acc := range cfg.ACCOUNTS {
		cfg.ACCOUNTS[i].RoomGroup = getRoomGroup(cfg.ROOMGROUPS, acc.Rg)
	}

	pp.Println(cfg.ACCOUNTS)

	// Rooms
	RunScript(cfg)

	jobs := cron.New(cron.WithSeconds())
	// jobs.AddFunc("20 */1 * * * *", func() {
	jobs.AddFunc(cfg.CRON_SCHEDULE, func() {
		RunScript(cfg)
	})
	jobs.Start()

	finished := make(chan bool)
	<-finished
}
