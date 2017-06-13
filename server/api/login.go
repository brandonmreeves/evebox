/* Copyright (c) 2017 Jason Ish
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 *
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED ``AS IS'' AND ANY EXPRESS OR IMPLIED
 * WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
 * DISCLAIMED. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT,
 * INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
 * (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
 * SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
 * STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING
 * IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
 * POSSIBILITY OF SUCH DAMAGE.
 */

package api

import (
	"github.com/jasonish/evebox/log"
	"github.com/jasonish/evebox/server/sessions"
	"net/http"
)

type LoginOptionsResponse struct {
	Authentication struct {
		Required bool     `json:"required"`
		Types    []string `json:"types,omitempty"`
	} `json:"authentication"`
	Message string `json:"login_message"`
}

func (c *ApiContext) LoginOptions(w *ResponseWriter, r *http.Request) error {
	response := LoginOptionsResponse{}
	config := c.appContext.Config

	if config.Authentication.Required {
		response.Authentication.Required = true

		response.Authentication.Types = append(response.Authentication.Types,
			config.Authentication.Type)

		response.Message = config.Authentication.LoginMessage

		if c.appContext.Config.Authentication.Github.Enabled {
			response.Authentication.Types =
				append(response.Authentication.Types, "github")
		}
	}

	return w.OkJSON(response)
}

type LoginSuccessResponse struct {
	SessionID string `json:"session_id"`
}

func (c *ApiContext) LoginHandler(w *ResponseWriter, r *http.Request) error {
	session, err := c.authenticator.Login(r)
	if err != nil {
		return ApiError{
			Status:  http.StatusUnauthorized,
			Message: "authentication failed",
		}
	}
	return w.OkJSON(LoginSuccessResponse{
		SessionID: session.Id,
	})
}

func (c *ApiContext) LogoutHandler(w *ResponseWriter, r *http.Request) error {
	session, ok := r.Context().Value("session").(*sessions.Session)
	if !ok {
		log.Debug("Logout request has no session")
	} else {
		c.sessionStore.Delete(session)
		log.Info("User %s from [%v] logged out", session.User.Username,
			r.RemoteAddr)
	}

	return w.Ok()
}
