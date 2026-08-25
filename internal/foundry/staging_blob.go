package foundry

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

// blobURISegments is how many path segments a blob URI carries: the container
// and the blob name.
const blobURISegments = 2

// blobURIParts is a staged blob URI split into what the Blob SDK addresses by.
type blobURIParts struct {
	Service   string
	Container string
	Blob      string
}

// parseBlobURI splits a full blob URI into service, container and blob name.
//
// The URI is built by stagedPackURI from a validated container URL, so a
// failure here means the container URL named no container.
func parseBlobURI(uri string) (blobURIParts, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return blobURIParts{}, fmt.Errorf("parse blob URI %q: %w", uri, err)
	}

	segments := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", blobURISegments)
	if len(segments) != blobURISegments || segments[0] == "" || segments[1] == "" {
		return blobURIParts{}, fmt.Errorf(
			"blob URI %q names no container and blob; staging_container should look "+
				"like https://acct.blob.core.windows.net/promptkit", uri)
	}

	return blobURIParts{
		Service:   u.Scheme + "://" + u.Host,
		Container: segments[0],
		Blob:      segments[1],
	}, nil
}

// StageObject writes data to a blob URI using the ambient Azure credential.
//
// The credential needs a data-plane role on the container -- Storage Blob Data
// Contributor is the usual one. Control-plane access to the Foundry project
// does not imply it, so a deploy that reaches here with only project access
// fails on the upload rather than at sign-in.
func (c *restClient) StageObject(ctx context.Context, uri string, data []byte) error {
	if c.cred == nil {
		return fmt.Errorf("no Azure credential available to stage %s", uri)
	}

	parts, err := parseBlobURI(uri)
	if err != nil {
		return err
	}

	client, err := azblob.NewClient(parts.Service, c.cred, nil)
	if err != nil {
		return fmt.Errorf("blob client for %s: %w", parts.Service, err)
	}

	if _, err := client.UploadBuffer(ctx, parts.Container, parts.Blob, data, nil); err != nil {
		return fmt.Errorf("upload %s to container %s: %w", parts.Blob, parts.Container, err)
	}
	return nil
}
