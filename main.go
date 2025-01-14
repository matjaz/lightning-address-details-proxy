package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	echologrus "github.com/davrux/echo-logrus/v4"
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	log "github.com/sirupsen/logrus"
)

type Config struct {
	SentryDSN   string `envconfig:"SENTRY_DSN"`
	LogFilePath string `envconfig:"LOG_FILE_PATH"`
	Port        int    `envconfig:"PORT" default:"3000"`
}

type LNResponse struct {
	Lnurlp  interface{} `json:"lnurlp"`
	Keysend interface{} `json:"keysend"`
	Nostr   interface{} `json:"nostr"`
}

type GIResponse struct {
	Invoice interface{} `json:"invoice"`
}

type GetJSONParams struct {
	url string
	wg  *sync.WaitGroup
}

func GetJSON(p GetJSONParams) (interface{}, *http.Response, error) {
	if p.wg != nil {
		defer p.wg.Done()
	}

	urlPrefix := "https://getalby.com"
	replacement := "http://alby-mainnet-getalbycom"

	url := strings.Replace(p.url, urlPrefix, replacement, 1)

	response, err := http.Get(url)
	if err != nil || response.StatusCode > 300 {
		return nil, response, fmt.Errorf("no details: %s - %v", p.url, err)
	} else {
		defer response.Body.Close()
		var j interface{}
		err = json.NewDecoder(response.Body).Decode(&j)
		if err != nil {
			return nil, response, fmt.Errorf("invalid JSON: %v", err)
		} else {
			return j, response, nil
		}
	}
}

func ToUrl(identifier string) (string, string, string, error) {
	parts := strings.Split(identifier, "@")
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("invalid lightning address %s", identifier)
	}

	keysendUrl := fmt.Sprintf("https://%s/.well-known/keysend/%s", parts[1], parts[0])
	lnurlpUrl := fmt.Sprintf("https://%s/.well-known/lnurlp/%s", parts[1], parts[0])
	nostrUrl := fmt.Sprintf("https://%s/.well-known/nostr.json?name=%s", parts[1], parts[0])

	return lnurlpUrl, keysendUrl, nostrUrl, nil
}

func main() {
	c := &Config{}
	logger := log.New()
	logger.SetFormatter(&log.JSONFormatter{})

	// Load configruation from environment variables
	err := godotenv.Load(".env")
	if err != nil {
		logger.Infof("Failed to load .env file: %v", err)
	}
	err = envconfig.Process("", c)
	if err != nil {
		logger.Fatalf("Error loading environment variables: %v", err)
	}

	e := echo.New()
	e.HideBanner = true
	echologrus.Logger = logger
	e.Use(echologrus.Middleware())

	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.CORS())

	// Setup exception tracking with Sentry if configured
	if c.SentryDSN != "" {
		if err = sentry.Init(sentry.ClientOptions{
			Dsn:          c.SentryDSN,
			IgnoreErrors: []string{"401"},
		}); err != nil {
			log.Printf("sentry init error: %v", err)
		}
		defer sentry.Flush(2 * time.Second)
		e.Use(sentryecho.New(sentryecho.Options{}))
	}

	e.GET("/lightning-address-details", func(c echo.Context) error {
		responseBody := &LNResponse{}
		var wg sync.WaitGroup
		var lnurlp, keysend, nostr interface{}
		var lnurlpResponse, keysendResponse, nostrResponse *http.Response

		ln := c.QueryParam("ln")
		lnurlpUrl, keysendUrl, nostrUrl, err := ToUrl(ln)
		if err != nil {
			logger.WithFields(log.Fields{
				"lightning_address": ln,
			}).Errorf("Failed to parse urls: %v", err)
			return c.JSON(http.StatusBadRequest, &responseBody)
		}

		wg.Add(3)

		go func() {
			lnurlp, lnurlpResponse, err = GetJSON(GetJSONParams{url: lnurlpUrl, wg: &wg})
			if err != nil {
				logger.WithFields(log.Fields{
					"lightning_address": ln,
					"lnurlp_url":        lnurlpUrl,
				}).Errorf("Failed to fetch lnurlp response: %v", err)
			} else {
				responseBody.Lnurlp = lnurlp
			}
		}()

		go func() {
			keysend, keysendResponse, err = GetJSON(GetJSONParams{url: keysendUrl, wg: &wg})
			if err != nil {
				logger.WithFields(log.Fields{
					"lightning_address": ln,
					"keysend_url":       keysendUrl,
				}).Errorf("Failed to fetch keysend response: %v", err)
			} else {
				responseBody.Keysend = keysend
			}
		}()

		go func() {
			nostr, nostrResponse, err = GetJSON(GetJSONParams{url: nostrUrl, wg: &wg})
			if err != nil {
				logger.WithFields(log.Fields{
					"lightning_address": ln,
					"nostr_url":         nostrUrl,
				}).Errorf("Failed to fetch nostr response: %v", err)
			} else {
				responseBody.Nostr = nostr
			}
		}()

		wg.Wait()

		// if the requests resulted in errors return a bad request. something must be wrong with the ln address
		if (lnurlpResponse == nil && keysendResponse == nil && nostrResponse == nil) ||
			(lnurlpResponse.StatusCode >= 300 && keysendResponse.StatusCode >= 300 && nostrResponse.StatusCode >= 300) {
			logger.WithFields(log.Fields{
				"lightning_address": ln,
			}).Errorf("Could not retrieve details for lightning address %v", ln)
			return c.JSON(http.StatusBadRequest, &responseBody)
		}

		c.Response().Header().Set(echo.HeaderCacheControl, lnurlpResponse.Header.Get("Cache-Control"))
		// default return response
		return c.JSONPretty(http.StatusOK, &responseBody, "  ")
	})

	e.GET("/generate-invoice", func(c echo.Context) error {
		responseBody := &GIResponse{}

		ln := c.QueryParam("ln")
		lnurlpUrl, _, _, err := ToUrl(ln)
		if err != nil {
			return c.JSON(http.StatusBadRequest, &responseBody)
		}

		lnurlp, lnurlpResponse, err := GetJSON(GetJSONParams{url: lnurlpUrl})
		if err != nil {
			logger.WithFields(log.Fields{
				"lightning_address": ln,
				"lnurlp_url":        lnurlpUrl,
			}).Errorf("Failed to fetch lnurlp response: %v", err)
		}

		// if the request resulted in error return a bad request. something must be wrong with the ln address
		if lnurlpResponse == nil {
			return c.JSON(http.StatusBadRequest, &responseBody)
		}
		// if the response have no success
		if lnurlpResponse != nil && lnurlpResponse.StatusCode > 300 {
			return c.JSONPretty(lnurlpResponse.StatusCode, &responseBody, "  ")
		}
		callback := lnurlp.(map[string]interface{})["callback"]

		// if the lnurlp response doesn't have a callback to generate invoice
		if callback == nil {
			return c.JSON(http.StatusBadRequest, &responseBody)
		}

		c.QueryParams().Del("ln")
		invoiceParams := c.QueryParams()
		invoiceUrl, err := url.Parse(callback.(string))
		if err != nil {
			logger.WithFields(log.Fields{
				"lightning_address": ln,
			}).Errorf("Failed to parse callback url: %v", err)
			return c.JSON(http.StatusBadRequest, &responseBody)
		}
		values := invoiceUrl.Query()
		for key, val := range invoiceParams {
			for _, v := range val {
				values.Add(key, v)
			}
		}
		invoiceUrl.RawQuery = values.Encode()
		invoice, invoiceResponse, err := GetJSON(GetJSONParams{url: invoiceUrl.String()})
		if err != nil {
			logger.WithFields(log.Fields{
				"lightning_address": ln,
			}).Errorf("Failed to fetch invoice: %v", err)
		} else {
			responseBody.Invoice = invoice
		}

		if invoiceResponse == nil {
			return c.JSON(http.StatusBadRequest, &responseBody)
		}
		if invoiceResponse != nil && invoiceResponse.StatusCode > 300 {
			return c.JSONPretty(lnurlpResponse.StatusCode, &responseBody, "  ")
		}

		// default return response
		return c.JSONPretty(http.StatusOK, &responseBody, "  ")
	})

	// Start server
	go func() {
		if err := e.Start(fmt.Sprintf(":%v", c.Port)); err != nil && err != http.ErrServerClosed {
			logger.Fatal("shutting down the server", err)
		}
	}()
	// Wait for interrupt signal to gracefully shutdown the server with a timeout of 10 seconds.
	// Use a buffered channel to avoid missing signals as recommended for signal.Notify
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		logger.Fatal(err)
	}

}
