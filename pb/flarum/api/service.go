package api

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	flarumcore "github.com/incurl/riff/pb/flarum/core"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/session"
)

// sessionCSRF verifies a mutating request's CSRF token.
//
// A failed check is a refusal, not a crash: the error is returned and
// the handler turns it into a 403.
func sessionCSRF(e *core.RequestEvent) error {
	return session.VerifyCSRF(e)
}

// slugPattern matches the characters a slug may contain.
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a title into a URL slug.
//
// The slug is the discussion's stable, URL-safe name; the id stays the
// identity.
func slugify(title string) string {
	slug := slugPattern.ReplaceAllString(strings.ToLower(title), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "discussion"
	}
	if len(slug) > 100 {
		slug = slug[:100]
		slug = strings.Trim(slug, "-")
	}
	return slug
}

// createDiscussion creates a discussion and its first post.
//
// The two are written together: a discussion without a first post would
// be invisible in the frontend, which renders the first post inline.
func (s *Server) createDiscussion(userID, title, content string) (*domain.Discussion, *domain.Post, error) {
	ctx := context.Background()

	contentHTML := s.renderContent(content)

	discussionID, postID, err := s.repo.CreateDiscussionWithPost(userID, title, slugify(title), content, contentHTML)
	if err != nil {
		return nil, nil, err
	}

	s.logger.Info("forum: discussion created",
		slog.String("discussion", discussionID),
		slog.String("actor", userID),
	)

	s.dispatch(ctx, "forum.discussion.created", userID, discussionID, map[string]any{"title": title})
	s.dispatch(ctx, "forum.post.created", userID, postID, map[string]any{"discussion": discussionID})

	discussion, err := s.repo.FindDiscussion(discussionID)
	if err != nil {
		return nil, nil, err
	}
	post, err := s.repo.FindPost(postID)
	if err != nil {
		return nil, nil, err
	}
	return discussion, post, nil
}

// createPost adds a reply to a discussion.
//
// The discussion's counters and its last-post pointer are updated in the
// same transaction, so a reader never sees a discussion whose last post
// is missing.
func (s *Server) createPost(userID, discussionID, content string) (*domain.Post, error) {
	ctx := context.Background()

	contentHTML := s.renderContent(content)

	postID, err := s.repo.CreatePost(userID, discussionID, content, contentHTML)
	if err != nil {
		return nil, err
	}

	s.logger.Info("forum: post created",
		slog.String("post", postID),
		slog.String("discussion", discussionID),
		slog.String("actor", userID),
	)

	s.dispatch(ctx, "forum.post.created", userID, postID, map[string]any{"discussion": discussionID})

	post, err := s.repo.FindPost(postID)
	if err != nil {
		return nil, err
	}
	return post, nil
}

// renderContent renders a post's Markdown source into the HTML the
// frontend displays.
//
// The frontend inserts `contentHtml` as trusted HTML, so the forum
// renders it server-side rather than letting the browser do it. A
// failure to render is not fatal: the source is served as-is, which is
// what a client without a renderer would show.
func (s *Server) renderContent(content string) string {
	if s.renderer == nil {
		return content
	}
	rendered, err := s.renderer.RenderHTML([]byte(content))
	if err != nil {
		s.logger.Error("forum: the post content could not be rendered",
			slog.String("error", err.Error()),
		)
		return content
	}
	return rendered
}

// dispatch fires an after-event.
//
// The bus is optional: the API server works without one, which is what
// the unit tests use.
func (s *Server) dispatch(ctx context.Context, name, actor, target string, data map[string]any) {
	if s.bus == nil {
		return
	}
	s.bus.DispatchAfter(ctx, flarumcore.Event{
		Name:   name,
		Actor:  actor,
		Target: target,
		Data:   data,
	})
}
