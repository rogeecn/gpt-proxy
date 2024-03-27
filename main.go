package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/spf13/viper"
)

type HandlerFunc func(http.ResponseWriter, *http.Request)

func (f HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f(w, r)
}

const (
	AuthHeader  = "Authorization"
	OrgHeader   = "OpenAI-Organization"
	TokenPrefix = "Bearer "
	OpenAIHost  = "https://api.openai.com"
)

const (
	ENV_BIND_PORT = "BIND_PORT"
	ENV_PROXY_URL = "PROXY_URL"
)

var (
	lock  sync.RWMutex
	users []string
)

func init() {
	viper.SetConfigFile("config.toml")
	viper.SetConfigType("toml")
	viper.AddConfigPath(".")
	viper.WatchConfig()
	viper.OnConfigChange(func(in fsnotify.Event) {
		if in.Op != fsnotify.Write {
			if err := viper.ReadInConfig(); err != nil {
				log.Fatal(errors.Wrap(err, "read config failed"))
			}
			lock.Lock()
			users = viper.GetStringSlice("users")
			lock.Unlock()
			log.Printf("config changed, user amount: %d", len(users))
		}
	})

	if err := viper.ReadInConfig(); err != nil {
		log.Fatal(errors.Wrap(err, "read config failed"))
	}

	lock.Lock()
	users = viper.GetStringSlice("users")
	lock.Unlock()
	log.Printf("load user amount: %d", len(users))
}

func main() {
	port := os.Getenv(ENV_BIND_PORT)
	if port == "" {
		port = viper.GetString("port")
		if port == "" {
			port = ":80"
		}
	}

	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	proxy, err := NewProxy(OpenAIHost)
	if err != nil {
		log.Fatal(err)
		return
	}
	checkAuth := HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authKey := r.Header.Get("Authorization")
		if authKey == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		authToken := string(authKey)
		authToken = strings.TrimPrefix(authToken, TokenPrefix)

		if !lo.Contains(users, authToken) {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// check user auths
		proxy.ServeHTTP(w, r)
	})

	log.Println("Starting server on ", port)
	if err := http.ListenAndServe(port, checkAuth); err != nil {
		log.Fatal(err)
	}
}

func NewProxy(targetHost string) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(targetHost)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(u)

	if proxyUrl := os.Getenv("PROXY_URL"); proxyUrl != "" {
		proxyURL, _ := url.Parse(proxyUrl)
		proxy.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
	}

	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)

		r.Host = u.Host

		if token := viper.GetString("token"); len(token) > 0 {
			r.Header.Set(AuthHeader, TokenPrefix+token)
		}

		if org := viper.GetString("org"); len(org) > 0 {
			r.Header.Set(OrgHeader, org)
		}
	}

	return proxy, nil
}
