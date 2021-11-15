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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	ocmprovider "github.com/cs3org/go-cs3apis/cs3/ocm/provider/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	ocm "github.com/cs3org/go-cs3apis/cs3/sharing/ocm/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/cs3org/reva/pkg/appctx"
	ctxpkg "github.com/cs3org/reva/pkg/ctx"
	"google.golang.org/grpc/metadata"

	"github.com/cs3org/reva/pkg/rgrpc/todo/pool"
)

type sendHandler struct {
	GatewaySvc string
}

func (h *sendHandler) init(c *Config) {
	h.GatewaySvc = c.GatewaySvc
}

func (h *sendHandler) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := appctx.GetLogger(r.Context())
		defer r.Body.Close()
		reqBody, err := io.ReadAll(r.Body)
		fmt.Printf("Got JSON body: '%s'", reqBody)
		if err != nil {
			log.Error().Msg("cannot read POST body!")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		reqMap := make(map[string]string)
		err = json.Unmarshal(reqBody, &reqMap)
		if err != nil {
			log.Error().Msg("cannot parse POST body!")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		loginType, loginUsername, loginPassword := reqMap["loginType"], reqMap["loginUsername"], reqMap["loginPassword"]
		path, recipientUsername, recipientHost := reqMap["path"], reqMap["recipientUsername"], reqMap["recipientHost"]

		// loginType := "basic"
		// loginUsername := "einstein"
		// loginPassword := "relativity"
		// path := "/home"
		// recipientUsername := "marie"
		// recipientHost := "localhost:17000"
		gatewayAddr := h.GatewaySvc
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
							Path: "http://" + recipientHost + "/",
						},
						Host:       recipientHost,
						ApiVersion: "1.0",
					},
					{
						Endpoint: &ocmprovider.ServiceEndpoint{
							Type: &ocmprovider.ServiceType{
								Name:        "WebDAV",
								Description: "WebDAV",
							},
							Name: "CESNET - WebDAV API",
							Path: "http://" + recipientHost + "/remote.php/webdav/",
						},
						Host:       recipientHost,
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
	})
}
