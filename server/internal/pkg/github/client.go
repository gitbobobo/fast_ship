package github

import (
	"context"
	"fmt"
	"os"

	gh "github.com/google/go-github/v62/github"
	"golang.org/x/oauth2"
)

func ptr[T any](v T) *T { return &v }

type Client struct {
	client *gh.Client
	owner  string
	repo   string
}

func NewClient(token, owner, repo string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &Client{
		client: gh.NewClient(tc),
		owner:  owner,
		repo:   repo,
	}
}

// CreateTag 创建 Git Tag（如果不存在）
func (c *Client) CreateTag(ctx context.Context, tag, commitish string) error {
	// 检查 tag 是否已存在
	_, _, err := c.client.Git.GetRef(ctx, c.owner, c.repo, "tags/"+tag)
	if err == nil {
		return nil // tag 已存在，跳过
	}

	ref := &gh.Reference{
		Ref:    ptr("refs/tags/" + tag),
		Object: &gh.GitObject{SHA: ptr(commitish)},
	}
	_, _, err = c.client.Git.CreateRef(ctx, c.owner, c.repo, ref)
	return err
}

// CreateRelease 创建 GitHub Release（如果不存在则创建，已存在则返回已有的）
func (c *Client) CreateRelease(ctx context.Context, tag, name, body string) (*gh.RepositoryRelease, error) {
	// 检查是否已存在
	release, _, err := c.client.Repositories.GetReleaseByTag(ctx, c.owner, c.repo, tag)
	if err == nil {
		return release, nil
	}

	rel := &gh.RepositoryRelease{
		TagName: ptr(tag),
		Name:    ptr(name),
		Body:    ptr(body),
	}
	release, _, err = c.client.Repositories.CreateRelease(ctx, c.owner, c.repo, rel)
	return release, err
}

// UploadAsset 上传文件到 Release
func (c *Client) UploadAsset(ctx context.Context, releaseID int64, filename string, file *os.File) error {
	// 先检查是否有同名 asset，有则删除
	assets, _, err := c.client.Repositories.ListReleaseAssets(ctx, c.owner, c.repo, releaseID, nil)
	if err == nil {
		for _, asset := range assets {
			if asset.GetName() == filename {
				_, _ = c.client.Repositories.DeleteReleaseAsset(ctx, c.owner, c.repo, asset.GetID())
				break
			}
		}
	}

	opts := &gh.UploadOptions{Name: filename}
	_, _, err = c.client.Repositories.UploadReleaseAsset(ctx, c.owner, c.repo, releaseID, opts, file)
	if err != nil {
		return fmt.Errorf("upload asset %s failed: %w", filename, err)
	}
	return nil
}
