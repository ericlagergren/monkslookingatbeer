package main

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/ChimeraCoder/anaconda"
	"github.com/garyburd/redigo/redis"
	"github.com/jzelinskie/geddit"
)

const ua = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/44.0.2403.130 Safari/537.36"

var (
	session = geddit.NewSession(ua)
	pool    = newPool("/tmp/monks.sock")
)

func init() {
	ck := os.Getenv("CONSUMERKEY")
	cs := os.Getenv("CONSUMERSECRET")

	if ck == "" || cs == "" {
		panic("Cannot have empty API key/secret/token")
	}

	anaconda.SetConsumerKey(ck)
	anaconda.SetConsumerSecret(cs)
}

func ParseRedistogoURL(u string) (string, string) {
	redisUrl := u
	redisInfo, _ := url.Parse(redisUrl)
	server := redisInfo.Host
	password := ""
	if redisInfo.User != nil {
		password, _ = redisInfo.User.Password()
	}
	return server, password
}

func newPool(socket string) *redis.Pool {
	url, pass := ParseRedistogoURL(os.Getenv("REDISTOGO_URL"))
	return &redis.Pool{
		MaxIdle:     3,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial(url, pass)
			if err != nil {
				return nil, err
			}
			if _, err := c.Do("AUTH", pass); err != nil {
				c.Close()
				return nil, err
			}
			return c, err
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
}

type tweets struct {
	api    *anaconda.TwitterApi
	tweets []*tweet
}
type tweet struct {
	title, img string
}

func CollectImages(api *anaconda.TwitterApi) {
	subs, err := session.SubredditSubmissions("monkslookingatbeer", geddit.NewSubmissions, geddit.ListingOptions{})
	if err != nil {
		panic(err)
	}

	t := &tweets{api: api}

	for _, v := range subs {
		if !v.IsSelf {
			if title, u := parse(v); u != nil && title != "" {
				if img := getImage(u); img != "" {
					if !havePosted(img) {
						t.tweets = append(t.tweets, &tweet{title: title, img: img})
					}
				}
			}
		}
	}
}

func havePosted(url string) bool {
	conn := pool.Get()
	defer conn.Close()

	reply, err := conn.Do("GET", url)
	if reply != nil && err == nil {
		b, ok := reply.([]byte)
		return ok && string(b) == url
	}
	return false
}

var reg = regexp.MustCompile("png|jpeg|jpg|gif")

func parse(sub *geddit.Submission) (string, *url.URL) {
	u, err := url.Parse(sub.URL)
	if err != nil {
		panic(err)
	}

	if !reg.Match([]byte(filepath.Ext(u.Path))) {

		if sub.Domain != "imgur.com" {
			return "", nil
		}

		// imgur/ddfdsf.jpg == imgur/ddfdsf.png
		u.Path = u.Path + ".jpg"
	}

	return sub.Title, u
}

func getImage(u *url.URL) string {
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("User-Agent", ua)
	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	cont, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	return base64.StdEncoding.EncodeToString(cont)
}

func (t *tweets) Tweet(status string, media anaconda.Media) error {
	v := url.Values{
		"media_ids": []string{media.MediaIDString},
	}
	_, err := t.api.PostTweet(status, v)
	if err != nil {
		fmt.Printf("Status: %q\n", status)
	}
	return err
}

func checkErr(err error) {
	if aerr, ok := err.(*anaconda.ApiError); ok {
		if isRateLimitError, nextWindow := aerr.RateLimitCheck(); isRateLimitError {
			<-time.After(nextWindow.Sub(time.Now()))
		}
	}
}

func addPost(title string) {
	conn := pool.Get()
	defer conn.Close()

	_, err := conn.Do("SETEX", title, 2592000, "t")
	if err != nil {
		panic(err)
	}
}

func (t *tweets) doTweets() {
	for _, tweet := range t.tweets {
		// If there are API errors just skip that post.
		media, err := t.api.UploadMedia(tweet.img)
		if err != nil {
			checkErr(err)
			continue
		}
		checkErr(t.Tweet(tweet.title, media))
		addPost(tweet.title)
	}
}

func main() {
	at := os.Getenv("ACCESSTOKEN")
	as := os.Getenv("ACCESSSECRET")

	if at == "" || as == "" {
		panic("Cannot have empty API key/secret/token")
	}

	api := anaconda.NewTwitterApi(at, as)

	api.EnableThrottling(5*time.Minute, 100)
	api.SetDelay(5 * time.Minute)

	ticker := time.NewTicker(6 * time.Hour)
	quit := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for {
			select {
			case <-ticker.C:
				fmt.Println("collecting images")
				CollectImages(api)
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
	wg.Wait()
}
