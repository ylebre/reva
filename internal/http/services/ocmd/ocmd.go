// Copyright 2018-2021 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package ocmd

import (
	"context"
	"net/http"
	"strconv"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	ocmprovider "github.com/cs3org/go-cs3apis/cs3/ocm/provider/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	ocm "github.com/cs3org/go-cs3apis/cs3/sharing/ocm/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	ctxpkg "github.com/cs3org/reva/pkg/ctx"
	"google.golang.org/grpc/metadata"

	"github.com/cs3org/reva/pkg/appctx"
	"github.com/cs3org/reva/pkg/rgrpc/todo/pool"
	"github.com/cs3org/reva/pkg/rhttp/global"
	"github.com/cs3org/reva/pkg/rhttp/router"
	"github.com/cs3org/reva/pkg/sharedconf"
	"github.com/cs3org/reva/pkg/smtpclient"
	"github.com/mitchellh/mapstructure"
	"github.com/rs/zerolog"
)

func init() {
	global.Register("ocmd", New)
}

// Config holds the config options that need to be passed down to all ocdav handlers
type Config struct {
	SMTPCredentials  *smtpclient.SMTPCredentials `mapstructure:"smtp_credentials"`
	Prefix           string                      `mapstructure:"prefix"`
	Host             string                      `mapstructure:"host"`
	GatewaySvc       string                      `mapstructure:"gatewaysvc"`
	MeshDirectoryURL string                      `mapstructure:"mesh_directory_url"`
	Config           configData                  `mapstructure:"config"`
}

func (c *Config) init() {
	c.GatewaySvc = sharedconf.GetGatewaySVC(c.GatewaySvc)

	// if c.Prefix == "" {
	// 	c.Prefix = "ocm"
	// }
}

type svc struct {
	Conf                 *Config
	SharesHandler        *sharesHandler
	NotificationsHandler *notificationsHandler
	ConfigHandler        *configHandler
	InvitesHandler       *invitesHandler
}

// New returns a new ocmd object
func New(m map[string]interface{}, log *zerolog.Logger) (global.Service, error) {

	conf := &Config{}
	if err := mapstructure.Decode(m, conf); err != nil {
		return nil, err
	}
	conf.init()

	s := &svc{
		Conf: conf,
	}
	s.SharesHandler = new(sharesHandler)
	s.NotificationsHandler = new(notificationsHandler)
	s.ConfigHandler = new(configHandler)
	s.InvitesHandler = new(invitesHandler)
	s.SharesHandler.init(s.Conf)
	s.NotificationsHandler.init(s.Conf)
	log.Debug().Str("initializing ConfigHandler Host", s.Conf.Host)

	s.ConfigHandler.init(s.Conf)
	s.InvitesHandler.init(s.Conf)

	return s, nil
}

// Close performs cleanup.
func (s *svc) Close() error {
	return nil
}

func (s *svc) Prefix() string {
	return s.Conf.Prefix
}

func (s *svc) Unprotected() []string {
	return []string{"/invites/accept", "/shares", "/ocm-provider", "/notifications", "/send"}
}

func (s *svc) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		ctx := r.Context()
		log := appctx.GetLogger(ctx)

		var head string
		head, r.URL.Path = router.ShiftPath(r.URL.Path)
		log.Debug().Str("head", head).Str("tail", r.URL.Path).Msg("http routing")

		switch head {
		case "ocm-provider":
			s.ConfigHandler.Handler().ServeHTTP(w, r)
			return
		case "shares":
			s.SharesHandler.Handler().ServeHTTP(w, r)
			return
		case "notifications":
			s.NotificationsHandler.Handler().ServeHTTP(w, r)
			return
		case "invites":
			s.InvitesHandler.Handler().ServeHTTP(w, r)
			return
		case "send":
			loginType := "basic"
			loginUsername := "einstein"
			loginPassword := "relativity"
			path := "/home"
			recipientUsername := "marie"
			recipientHost := "localhost:17000"
			gatewayAddr := s.Conf.GatewaySvc
			gatewayClient, err := pool.GetGatewayServiceClient(gatewayAddr)
			if err != nil {
				log.Error().Msg("cannot get grpc client!")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			loginReq := &gateway.AuthenticateRequest{
				Type:         loginType,
				ClientId:     loginUsername,
				ClientSecret: loginPassword,
			}

			loginCtx := context.Background()
			res, err := gatewayClient.Authenticate(loginCtx, loginReq)
			if err != nil {
				log.Error().Msg("error logging in")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			authCtx := context.Background()

			authCtx = ctxpkg.ContextSetToken(authCtx, res.Token)
			authCtx = metadata.AppendToOutgoingContext(authCtx, ctxpkg.TokenHeader, res.Token)

			// copied from cmd/reva/public-share-create.go:
			ref := &provider.Reference{Path: path}

			req := &provider.StatRequest{Ref: ref}
			res2, err := gatewayClient.Stat(authCtx, req)
			if err != nil {
				log.Error().Msg("error sending: stat file/folder to share")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			if res2.Status.Code != rpc.Code_CODE_OK {
				log.Error().Msg("error returned: stat file/folder to share")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			// see cmd/reva/share-creat.go:getSharePerm
			readerPermission := &provider.ResourcePermissions{
				GetPath:              true,
				InitiateFileDownload: true,
				ListFileVersions:     true,
				ListContainer:        true,
				Stat:                 true,
			}

			grant := &ocm.ShareGrant{
				Permissions: &ocm.SharePermissions{
					Permissions: readerPermission,
				},
				Grantee: &provider.Grantee{
					Type: provider.GranteeType_GRANTEE_TYPE_USER,
					Id: &provider.Grantee_UserId{
						UserId: &userpb.UserId{
							Idp:      recipientHost,
							OpaqueId: recipientUsername,
						},
					},
				},
			}
			shareRequest := &ocm.CreateOCMShareRequest{
				Opaque: &types.Opaque{
					Map: map[string]*types.OpaqueEntry{
						"permissions": {
							Decoder: "plain",
							Value:   []byte(strconv.Itoa(0)),
						},
						"name": {
							Decoder: "plain",
							Value:   []byte(path),
						},
					},
				},
				ResourceId: res2.Info.Id,
				Grant:      grant,
				RecipientMeshProvider: &ocmprovider.ProviderInfo{
					Name:         "oc-cesnet",
					FullName:     "ownCloud@CESNET",
					Description:  "OwnCloud has been designed for individual users.",
					Organization: "CESNET",
					Domain:       "cesnet.cz",
					Homepage:     "https://owncloud.cesnet.cz",
					Email:        "ownCloud@CESNET",
					Services: []*ocmprovider.Service{
						{
							Endpoint: &ocmprovider.ServiceEndpoint{
								Type: &ocmprovider.ServiceType{
									Name:        "OCM",
									Description: "Open Cloud Mesh",
								},
								Name: "CESNET - OCM API",
								Path: "http://127.0.0.1:17001/ocm/",
							},
							Host:       "localhost:17000",
							ApiVersion: "1.0",
						},
						{
							Endpoint: &ocmprovider.ServiceEndpoint{
								Type: &ocmprovider.ServiceType{
									Name:        "WebDAV",
									Description: "WebDAV",
								},
								Name: "CESNET - WebDAV API",
								Path: "http://127.0.0.1:17001/remote.php/webdav/",
							},
							Host:       "localhost:17000",
							ApiVersion: "1.0",
						},
					},
				},
			}

			shareRes, err := gatewayClient.CreateOCMShare(authCtx, shareRequest)
			if err != nil {
				log.Error().Msg("error sending: CreateShare")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			if shareRes.Status.Code != rpc.Code_CODE_OK {
				log.Error().Msg("error returned: CreateShare")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusOK)
			return
		}

		log.Warn().Msg("resource not found")
		w.WriteHeader(http.StatusNotFound)
	})
}
