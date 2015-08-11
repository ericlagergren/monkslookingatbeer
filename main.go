package main

import (
	"encoding/base64"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ChimeraCoder/anaconda"
	"github.com/garyburd/redigo/redis"
	"github.com/jzelinskie/geddit"
)

const ua = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/44.0.2403.130 Safari/537.36"

var (
	api     *anaconda.TwitterApi
	session = geddit.NewSession(ua)
	pool    = newPool("/tmp/monks.sock")
)

func init() {
	ck := os.Getenv("CONSUMERKEY")
	cs := os.Getenv("CONSUMERSECRET")
	at := os.Getenv("ACCESSTOKEN")
	as := os.Getenv("ACCESSSECRET")

	if ck == "" || cs == "" || at == "" || as == "" {
		panic("Cannot have empty API key/secret/token")
	}

	anaconda.SetConsumerKey(ck)
	anaconda.SetConsumerSecret(cs)
	api = anaconda.NewTwitterApi(at, as)
}

func newPool(socket string) *redis.Pool {
	createSocket(socket, 0755)

	return &redis.Pool{
		MaxIdle:     3,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("unix", socket)
			if err != nil {
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

// createSocket will create a socket with the given perms
// if it does not currently exist.
func createSocket(name string, perms int) {
	if _, err := os.Stat(name); os.IsNotExist(err) {
		file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, os.FileMode(perms))
		if err != nil {
			panic(err)
		}
		if err := file.Close(); err != nil {
			panic(err)
		}
	}
}

type tweets []*tweet
type tweet struct {
	title, img string
}

func CollectImages() {
	subs, err := session.SubredditSubmissions("monkslookingatbeer", geddit.NewSubmissions, geddit.ListingOptions{})
	if err != nil {
		panic(err)
	}

	var t []*tweet

	for _, v := range subs {
		if !v.IsSelf {
			if title, u := parse(v); u != nil && title != "" {
				if img := getImage(u); img != "" {
					if !havePosted(img) {
						t = append(t, &tweet{title: title, img: img})
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

func Tweet(status string, media anaconda.Media) error {
	v := url.Values{
		"media_ids": []string{media.MediaIDString},
	}
	_, err := api.PostTweet(status, v)
	return err
}

func checkErr(err error) {
	if aerr, ok := err.(*anaconda.ApiError); ok {
		if isRateLimitError, nextWindow := aerr.RateLimitCheck(); isRateLimitError {
			<-time.After(nextWindow.Sub(time.Now()))
		}
	}
}

func addURL(url string) {
	conn := pool.Get()
	defer conn.Close()

	_, err := conn.Do("SET", url)
	if err != nil {
		panic(err)
	}
}

func (t *tweets) doTweets() {
	for _, tweet := range *t {
		media, err := Upload(tweet.img)
		if err != nil {
			checkErr(err)
		}
		err = Tweet(tweet.title, media)
		checkErr(err)
		addURL(tweet.img)
	}
}

func Upload(img string) (anaconda.Media, error) {
	return api.UploadMedia(img)
}

func main() {
	api.SetDelay(5 * time.Minute)
	api.EnableThrottling(5*time.Minute, 100)
	CollectImages()
}
