/*
   Copyright (c) 2016 VMware, Inc. All Rights Reserved.
   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package service

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/vmware/harbor/api"
	"github.com/vmware/harbor/dao"
	"github.com/vmware/harbor/models"
	"github.com/vmware/harbor/service/cache"
	"github.com/vmware/harbor/utils/log"

	"github.com/astaxie/beego"
)

// NotificationHandler handles request on /service/notifications/, which listens to registry's events.
type NotificationHandler struct {
	beego.Controller
}

const manifestPattern = `^application/vnd.docker.distribution.manifest.v\d\+json`

// Post handles POST request, and records audit log or refreshes cache based on event.
func (n *NotificationHandler) Post() {
	var notification models.Notification
	log.Infof("request body in string: %s", string(n.Ctx.Input.CopyBody(1<<32)))
	err := json.Unmarshal(n.Ctx.Input.CopyBody(1<<32), &notification)

	if err != nil {
		log.Errorf("failed to decode notification: %v", err)
		return
	}

	events, err := filterEvents(&notification)
	if err != nil {
		log.Errorf("failed to filter events: %v", err)
		return
	}

	for _, event := range events {
		repository := event.Target.Repository

		project := ""
		if strings.Contains(repository, "/") {
			project = repository[0:strings.LastIndex(repository, "/")]
		}

		tag := event.Target.Tag
		action := event.Action

		user := event.Actor.Name
		if len(user) == 0 {
			user = "anonymous"
		}

		go dao.AccessLog(user, project, repository, tag, action)
		if action == "push" || action == "delete" {
			go func() {
				if err := cache.RefreshCatalogCache(); err != nil {
					log.Errorf("failed to refresh cache: %v", err)
				}
			}()

			operation := ""
			if action == "push" {
				operation = models.RepOpTransfer
			} else {
				operation = models.RepOpDelete
			}

			go api.TriggerReplicationByRepository(repository, []string{tag}, operation)
		}
	}
}

func filterEvents(notification *models.Notification) ([]*models.Event, error) {
	events := []*models.Event{}

	for _, event := range notification.Events {

		//delete
		// TODO add tag field
		if event.Action == "delete" {
			events = append(events, &event)
			continue
		}

		isManifest, err := regexp.MatchString(manifestPattern, event.Target.MediaType)
		if err != nil {
			log.Errorf("failed to match the media type against pattern: %v", err)
			continue
		}

		if !isManifest {
			continue
		}

		//pull and push manifest by docker-client
		if strings.HasPrefix(event.Request.UserAgent, "docker") && (event.Action == "pull" || event.Action == "push") {
			events = append(events, &event)
			continue
		}

		//push manifest by docker-client or job-service
		if strings.ToLower(strings.TrimSpace(event.Request.UserAgent)) == "harbor-registry-client" && event.Action == "push" {
			events = append(events, &event)
			continue
		}
	}

	return events, nil
}

// Render returns nil as it won't render any template.
func (n *NotificationHandler) Render() error {
	return nil
}
