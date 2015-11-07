package remote

import (
	"encoding/gob"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/remote_api"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/user"
	"strings"
)

////////////////////////////////////////////////////////////////////////////
// This is for reading app.yaml
////////////////////////////////////////////////////////////////////////////

type App struct {
	Application string          `yaml:"application"`
	Version     string          `yaml:"version"`
	Runtime     string          `yaml:"runtime"`
	APIVersion  string          `yaml:"api_version"`
	Handlers    []AppURLHandler `yaml:"handlers"`
}

type AppURLHandler struct {
	URL         string `yaml:"url"`
	StaticDir   string `yaml:"static_dir"`
	StaticFiles string `yaml:"static_files"`
	Upload      string `yaml:"upload"`
	Script      string `yaml:"script"`
}

func readApp(path string) (app *App, err error) {
	bytes, err := ioutil.ReadFile(path + "/app.yaml")
	if err == nil {
		app = &App{}
		err = yaml.Unmarshal(bytes, app)
		if err == nil {
			return app, nil
		}
	}
	return nil, err
}

//////////////////////////////////////////////////////////////
// A Session represents a connection to an App Engine instance
//////////////////////////////////////////////////////////////

type Session struct {
	Local         bool
	Source        string
	ServiceHost   string
	ServiceScheme string
	ServiceURL    *url.URL
	AppHost       string
	AppScheme     string
	AppURL        *url.URL
	App           *App
	client        *http.Client
}

type CookieTray struct {
	Cookies []*http.Cookie
	Path    string
}

func NewSession(path string, local bool) (session *Session, err error) {

	app, err := readApp(path)

	// build the session object
	session = &Session{
		Local: local,
		App:   app,
	}
	if local {
		session.ServiceHost = "localhost:8000"
		session.ServiceScheme = "http"
		session.AppHost = "localhost:8080"
		session.AppScheme = "http"
	} else {
		session.ServiceHost = "appengine.google.com"
		session.ServiceScheme = "https"
		session.AppHost = app.Application + ".appspot.com"
		session.AppScheme = "https"
	}
	session.ServiceURL, err = url.Parse(session.ServiceScheme + "://" + session.ServiceHost)
	if err != nil {
		return nil, err
	}
	session.AppURL, err = url.Parse(session.AppScheme + "://" + session.AppHost)
	if err != nil {
		return nil, err
	}

	// try to read a cookie file, but if we fail, go on without it
	jar, err := cookiejar.New(nil)
	var cookieTrays []CookieTray
	f, readerr := os.Open(session.cookieFileName())
	if readerr == nil {
		dec := gob.NewDecoder(f)
		readerr = dec.Decode(&cookieTrays)
		if readerr == nil {
			for _, cookieTray := range cookieTrays {
				url, err := url.Parse(cookieTray.Path)
				if err == nil {
					jar.SetCookies(url, cookieTray.Cookies)
				}
			}
		}
	}

	// finish by creating the session client
	session.client = &http.Client{
		Jar: jar,
	}
	return session, err
}

func (session *Session) appValues() (values *url.Values) {
	values = &url.Values{}
	values.Set("app_id", session.App.Application)
	values.Set("version", session.App.Version)
	return values
}

func (session *Session) cookieFileName() (name string) {
	u, _ := user.Current()
	return u.HomeDir + "/.cookies"
}

// Read credentials from standard input
func readCredentials() (username string, password string, err error) {
	state, err := terminal.MakeRaw(0)
	if err == nil {
		t := terminal.NewTerminal(os.Stdin, "Username: ")
		username, err = t.ReadLine()
		if err == nil {
			password, err = t.ReadPassword("Password: ")
		}
		terminal.Restore(0, state)
	}
	return username, password, nil
}

// This signs us in so that we can use the remote_api.
// To do that we need a client object that has cookies that are set by responses to login requests.
func (session *Session) Signin() (err error) {
	username, password, err := readCredentials()

	// create the http client that we'll use to make signin connections
	redirectPolicyFunc := func(req *http.Request, via []*http.Request) (err error) {
		return errors.New("don't follow redirects")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		CheckRedirect: redirectPolicyFunc,
		Jar:           jar,
	}

	// if we're connecting to a non-local app, first authenticate with Google
	var values map[string]string
	if !session.Local {
		v := url.Values{}
		v.Set("Email", username)
		v.Set("Passwd", password)
		v.Set("source", "Google-appcfg-1.9.17")
		v.Set("accountType", "HOSTED_OR_GOOGLE")
		v.Set("service", "ah")
		response, err := http.Get("https://www.google.com/accounts/ClientLogin?" + v.Encode())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		contents, err := ioutil.ReadAll(response.Body)
		if err != nil {
			return err
		}
		values = make(map[string]string)
		lines := strings.Split(string(contents), "\n")
		for _, line := range lines {
			keyvalue := strings.Split(line, "=")
			if len(keyvalue) == 2 {
				values[keyvalue[0]] = keyvalue[1]
			}
		}
		fmt.Printf("RECEIVED: %+v\n", values)
		errorMessage, hasError := values["Error"]
		if hasError {
			return errors.New(errorMessage)
		}
	}

	// fetch the service and app login paths to get necessary cookies
	v2 := url.Values{}
	v2.Set("continue", "http://localhost")
	v2.Set("auth", values["Auth"])

	if session.Local {
		v2.Set("admin", "True")
		v2.Set("action", "Login")
		v2.Set("email", username)
	}
	_, err = client.Get(session.ServiceScheme + "://" + session.ServiceHost + "/_ah/login?" + v2.Encode())
	if len(session.App.Application) > 0 {
		_, err = client.Get(session.AppScheme + "://" + session.AppHost + "/_ah/login?" + v2.Encode())
	}

	// save the cookies locally
	cookieTrays := []CookieTray{}
	cookieTrays = append(cookieTrays, CookieTray{Path: session.AppScheme + "://" + session.AppHost, Cookies: jar.Cookies(session.AppURL)})
	cookieTrays = append(cookieTrays, CookieTray{Path: session.ServiceScheme + "://" + session.ServiceHost, Cookies: jar.Cookies(session.ServiceURL)})
	f, err := os.Create(session.cookieFileName())
	defer f.Close()
	enc := gob.NewEncoder(f)
	return enc.Encode(cookieTrays)
}

func (session *Session) Signout() (err error) {
	return os.Remove(session.cookieFileName())
}

/////////////////////////////////////////////////
// This allows us to use the remote_api
/////////////////////////////////////////////////
func (session *Session) Context() (c context.Context, err error) {
	return remote_api.NewRemoteContext(session.AppHost, session.client)
}

/////////////////////////////////////////////////
// Get database info using the datastore API
/////////////////////////////////////////////////

func (session *Session) DatastoreInfo() (err error) {
	c, err := session.Context()
	if err != nil {
		return
	}
	log.Printf("App ID %q", appengine.AppID(c))
	return
}
