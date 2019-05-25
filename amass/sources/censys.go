// Copyright 2017 Jeff Foley. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OWASP/Amass/amass/core"
	"github.com/OWASP/Amass/amass/utils"
)

// Censys is the Service that handles access to the Censys data source.
type Censys struct {
	core.BaseService

	API        *core.APIKey
	SourceType string
	RateLimit  time.Duration
}

// NewCensys returns he object initialized, but not yet started.
func NewCensys(config *core.Config, bus *core.EventBus) *Censys {
	c := &Censys{
		SourceType: core.CERT,
		RateLimit:  3 * time.Second,
	}

	c.BaseService = *core.NewBaseService(c, "Censys", config, bus)
	return c
}

// OnStart implements the Service interface
func (c *Censys) OnStart() error {
	c.BaseService.OnStart()

	c.API = c.Config().GetAPIKey(c.String())
	if c.API == nil || c.API.Key == "" || c.API.Secret == "" {
		c.Config().Log.Printf("%s: API key data was not provided", c.String())
	}

	go c.processRequests()
	return nil
}

func (c *Censys) processRequests() {
	last := time.Now()

	for {
		select {
		case <-c.Quit():
			return
		case req := <-c.RequestChan():
			if c.Config().IsDomainInScope(req.Domain) {
				if time.Now().Sub(last) < c.RateLimit {
					time.Sleep(c.RateLimit)
				}
				last = time.Now()
				if c.API != nil && c.API.Key != "" && c.API.Secret != "" {
					c.apiQuery(req.Domain)
				} else {
					c.executeQuery(req.Domain)
				}
				last = time.Now()
			}
		}
	}
}

type censysRequest struct {
	Query  string   `json:"query"`
	Page   int      `json:"page"`
	Fields []string `json:"fields"`
}

func (c *Censys) apiQuery(domain string) {
	re := c.Config().DomainRegex(domain)
	if re == nil {
		return
	}

	for page := 1; ; page++ {
		c.SetActive()
		jsonStr, err := json.Marshal(&censysRequest{
			Query:  domain,
			Page:   page,
			Fields: []string{"parsed.subject_dn"},
		})
		if err != nil {
			break
		}

		u := c.apiURL()
		body := bytes.NewBuffer(jsonStr)
		headers := map[string]string{"Content-Type": "application/json"}
		resp, err := utils.RequestWebPage(u, body, headers, c.API.Key, c.API.Secret)
		if err != nil {
			c.Config().Log.Printf("%s: %s: %v", c.String(), u, err)
			break
		}
		// Extract the subdomain names from the certificate information
		var m struct {
			Status   string `json:"status"`
			Metadata struct {
				Page  int `json:"page"`
				Pages int `json:"pages"`
			} `json:"metadata"`
			Results []struct {
				Data string `json:"parsed.subject_dn"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(resp), &m); err != nil || m.Status != "ok" {
			break
		}

		if len(m.Results) != 0 {
			for _, result := range m.Results {
				for _, name := range re.FindAllString(result.Data, -1) {
					c.Bus().Publish(core.NewNameTopic, &core.Request{
						Name:   utils.RemoveAsteriskLabel(name),
						Domain: domain,
						Tag:    c.SourceType,
						Source: c.String(),
					})
				}
			}
		}

		if m.Metadata.Page >= m.Metadata.Pages {
			break
		}
		time.Sleep(c.RateLimit)
	}
}

func (c *Censys) apiURL() string {
	return "https://www.censys.io/api/v1/search/certificates"
}

func (c *Censys) executeQuery(domain string) {
	var err error
	var url, page string

	re := c.Config().DomainRegex(domain)
	if re == nil {
		return
	}

	c.SetActive()
	url = c.webURL(domain)
	page, err = utils.RequestWebPage(url, nil, nil, "", "")
	if err != nil {
		c.Config().Log.Printf("%s: %s: %v", c.String(), url, err)
		return
	}

	for _, sd := range re.FindAllString(page, -1) {
		c.Bus().Publish(core.NewNameTopic, &core.Request{
			Name:   utils.RemoveAsteriskLabel(cleanName(sd)),
			Domain: domain,
			Tag:    c.SourceType,
			Source: c.String(),
		})
	}
}

func (c *Censys) webURL(domain string) string {
	return fmt.Sprintf("https://www.censys.io/domain/%s/table", domain)
}
