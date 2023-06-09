package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type Config struct {
  SentryDSN             string `envconfig:"SENTRY_DSN"`
  LogFilePath           string `envconfig:"LOG_FILE_PATH"`
  Port                  int    `envconfig:"PORT" default:"3000"`
}

type ErrorResponse struct {
	Status int `json:"status"`
	Message string `json:"message"`
}

type LNResponse struct {
    Lnurlp interface{} `json:"lnurlp"`
    Keysend interface{} `json:"keysend"`
    Nostr interface{} `json:"nostr"`
    Error ErrorResponse `json:"error"`
}

type GIResponse struct {
	Invoice interface{} `json:"invoice"`
}

type GetJSONParams struct {
	url string
	wg *sync.WaitGroup
}

func GetJSON(p GetJSONParams) (interface{}, *http.Response, error) {
	if p.wg != nil {
		defer p.wg.Done()
	}
  response, err := http.Get(p.url)
  if err != nil || response.StatusCode > 300  {
    return nil, response, fmt.Errorf("No details: %s - %v", p.url, err)
  } else {
    defer response.Body.Close()
    var j interface{}
    err = json.NewDecoder(response.Body).Decode(&j)
    if err != nil {
      return nil, response, fmt.Errorf("Invalid JSON: %v", err)
    } else {
      return j, response, nil
    }
  }
}

func ToUrl(identifier string) (string, string, string, error) {
  parts := strings.Split(identifier, "@")
  if len(parts) != 2 {
    return "", "", "", fmt.Errorf("Invalid lightning address %s", identifier)
  }

  keysendUrl := fmt.Sprintf("https://%s/.well-known/keysend/%s", parts[1], parts[0])
  lnurlpUrl := fmt.Sprintf("https://%s/.well-known/lnurlp/%s", parts[1], parts[0])
  nostrUrl := fmt.Sprintf("https://%s/.well-known/nostr.json?name=%s", parts[1], parts[0])

  return lnurlpUrl, keysendUrl, nostrUrl, nil
}

func main() {
  c := &Config{}

  // Load configruation from environment variables
  err := godotenv.Load(".env")
  if err != nil {
    fmt.Println("Failed to load .env file")
  }
  err = envconfig.Process("", c)
  if err != nil {
    log.Fatalf("Error loading environment variables: %v", err)
  }

  e := echo.New()
  e.HideBanner = true
  e.Use(middleware.Logger())
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
		var lnurlpError error

    ln := c.QueryParam("ln")
    lnurlpUrl, keysendUrl, nostrUrl, err := ToUrl(ln)
    if err != nil {
      return c.JSON(http.StatusBadRequest, &responseBody)
    }

		wg.Add(3)

		go func() {
			lnurlp, lnurlpResponse, lnurlpError = GetJSON(GetJSONParams{url: lnurlpUrl, wg: &wg})
			if lnurlpError != nil {
				e.Logger.Errorf("%v", lnurlpError)
			} else {
				responseBody.Lnurlp = lnurlp
			}
		}()
	
		go func() {
			keysend, keysendResponse, err = GetJSON(GetJSONParams{url: keysendUrl, wg: &wg})
			if err != nil {
				e.Logger.Errorf("%v", err)
			} else {
				responseBody.Keysend = keysend
			}
		}()

		go func() {
			nostr, nostrResponse, err = GetJSON(GetJSONParams{url: nostrUrl, wg: &wg})
			if err != nil {
				e.Logger.Errorf("%v", err)
			} else {
				responseBody.Nostr = nostr
			}
		}()

		wg.Wait()

    // if the requests resulted in errors return a bad request. something must be wrong with the ln address
    if ((lnurlpResponse == nil && keysendResponse == nil && nostrResponse == nil) ||
		(lnurlpResponse.StatusCode >= 300 && keysendResponse.StatusCode >= 300 && nostrResponse.StatusCode >= 300)) {
			e.Logger.Errorf("Could not retrieve details for lightning address %v", ln)
			responseBody.Error = ErrorResponse {
				Status: lnurlpResponse.StatusCode,
				Message: lnurlpError.Error(),
			}
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
      e.Logger.Errorf("%v", err)
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
		invoiceParams := c.QueryParams().Encode()

		invoice, invoiceResponse, err := GetJSON(GetJSONParams{url: callback.(string) + "?" + invoiceParams});
		if err != nil {
			e.Logger.Errorf("%v", err)
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
      e.Logger.Fatal("shutting down the server", err)
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
    e.Logger.Fatal(err)
  }

}
