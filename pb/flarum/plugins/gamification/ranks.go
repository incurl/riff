package gamification

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/plugin"
)

// The ranks, as upstream's `Api\Resource\RankResource` serves them.
//
// A rank is a name, a colour and the number of points needed to reach it.
// `ranks` is the JSON:API type, and it is what BOTH the admin editor and
// the `user.ranks` relationship are keyed by.
//
// Upstream's endpoints are:
//
//	GET    /api/ranks        (Index — no admin gate: the ranks are shown
//	                          next to a member's name, so everybody who
//	                          reads a post needs them)
//	POST   /api/ranks        (Create — admin)
//	PATCH  /api/ranks/{id}   (Update — admin)
//	DELETE /api/ranks/{id}   (Delete — admin)
//
// THE THREE WRITES ARE REGISTERED WITH A POST TWIN. PocketBase bakes the
// method into the route pattern, and the admin application rewrites every
// PATCH/DELETE into a POST carrying `X-HTTP-Method-Override` — so a
// PATCH-only route is unreachable from the admin UI, and an edit would
// answer 404 for a request that is perfectly ordinary. The twin resolves
// the effective method itself (see `api.EffectiveMethod`).

// rank is one rank row.
//
// It is a value rather than a PocketBase record so the handlers, the
// serializer and the user relationship all read the same shape.
type rank struct {
	// ID is the record id.
	ID string
	// Name is the label shown next to a member's name.
	Name string
	// Color is the badge's colour.
	Color string
	// Points is the number of points needed to reach the rank.
	Points int
}

// rankResource renders a rank as a JSON:API resource.
//
// It is upstream's `RankResource::fields()`, attribute for attribute, and
// the names are what the frontend's `Rank` model reads (`points()`,
// `name()`, `color()`).
func rankResource(r rank) api.Resource {
	return api.Resource{
		Type: TypeRank,
		ID:   r.ID,
		Attributes: map[string]any{
			"name":   r.Name,
			"color":  r.Color,
			"points": r.Points,
		},
	}
}

// pluginRankResource is the same resource in the plugin package's own
// shape, which is what the `included` walk consumes.
func pluginRankResource(r rank) *plugin.Resource {
	return &plugin.Resource{
		Type: TypeRank,
		ID:   r.ID,
		Attributes: map[string]any{
			"name":   r.Name,
			"color":  r.Color,
			"points": r.Points,
		},
	}
}

// listRanksHandler serves GET /api/ranks.
//
// The order is the ladder's: fewest points first. Upstream's
// `RankResource::sorts()` is empty (the client may not reorder a rank
// ladder), and the admin page renders the list in the order it arrives —
// so the order has to be the points, or a rank editor shows an arbitrary
// arrangement.
func (p *Plugin) listRanksHandler(e *core.RequestEvent) error {
	ranks, err := listRanks(e.App, p.physical[ranksCollection])
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The ranks could not be loaded.")
	}

	resources := make([]api.Resource, 0, len(ranks))
	for _, r := range ranks {
		resources = append(resources, rankResource(r))
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: resources})
}

// createRankHandler serves POST /api/ranks.
//
// It is upstream's `Endpoint\Create::make()->admin()`: a rank is a forum
// configuration row, not a member's own data.
func (p *Plugin) createRankHandler(e *core.RequestEvent) error {
	if err := requireAdmin(e); err != nil {
		return err
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	fields, err := rankBody(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The request body could not be read.")
	}

	// Upstream marks all three `requiredOnCreate`: a rank with no name
	// cannot be rendered, one with no colour has nothing to draw, and one
	// with no threshold is reachable by everybody at once.
	name, ok := fields.name()
	if !ok || name == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A rank needs a name.")
	}

	points, ok := fields.points()
	if !ok {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A rank needs a points threshold.")
	}

	created, err := saveRank(e.App, p.physical[ranksCollection], "", rank{
		Name:   name,
		Color:  fields.colorOr(""),
		Points: points,
	})
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The rank could not be saved.")
	}
	return api.WriteDocument(e, http.StatusCreated, api.Document{Data: rankResource(created)})
}

// updateRanksHandler serves PATCH and DELETE on /api/ranks/{id}.
//
// Both verbs land here because the admin application sends a DELETE as a
// POST carrying `X-HTTP-Method-Override: DELETE`: the route pattern
// matches the REAL method, so the POST half has to resolve the intended
// one itself. A POST that names no override is a method error rather than
// an update, because guessing would let a stray POST delete a rank.
func (p *Plugin) updateRanksHandler(e *core.RequestEvent) error {
	if err := requireAdmin(e); err != nil {
		return err
	}

	method := api.EffectiveMethod(e)
	if method != http.MethodPatch && method != http.MethodDelete {
		return api.WriteMethodError(e, errRankMethod)
	}

	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	id := e.Request.PathValue("id")
	if id == "" {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That rank does not exist.")
	}

	existing, err := findRank(e.App, p.physical[ranksCollection], id)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That rank does not exist.")
	}

	if method == http.MethodDelete {
		return p.deleteRank(e, existing)
	}

	// A PATCH names only the fields it changes: an absent attribute keeps
	// its stored value, which is what `Model.save({ name })` sends.
	fields, err := rankBody(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The request body could not be read.")
	}

	if name, ok := fields.name(); ok {
		if name == "" {
			return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A rank needs a name.")
		}
		existing.Name = name
	}
	if points, ok := fields.points(); ok {
		existing.Points = points
	}
	if color, ok := fields.color(); ok {
		existing.Color = color
	}

	updated, err := saveRank(e.App, p.physical[ranksCollection], existing.ID, existing)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The rank could not be saved.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{Data: rankResource(updated)})
}

// deleteRank removes a rank.
func (p *Plugin) deleteRank(e *core.RequestEvent, r rank) error {
	collection, err := e.App.FindCollectionByNameOrId(p.physical[ranksCollection])
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The rank could not be deleted.")
	}

	record, err := e.App.FindRecordById(collection, r.ID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That rank does not exist.")
	}

	if err := e.App.Delete(record); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The rank could not be deleted.")
	}

	return api.WriteEmpty(e, http.StatusNoContent)
}

// requireAdmin refuses a caller who does not administer the forum.
//
// Upstream's endpoints declare `->admin()`, which is the administrator
// GROUP or the instance's operator. The forum models both in
// `session.Actor.IsAdmin` (the statistics plugin gates its own
// admin-only endpoint the same way), so the check is the same one every
// other administrator-only route makes.
func requireAdmin(e *core.RequestEvent) error {
	actor, err := api.CurrentActor(e)
	if err != nil || actor.User == nil || !actor.User.IsAdmin {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to manage ranks.")
	}

	return nil
}

// rankFields is a decoded rank body with the attributes it named.
//
// A field that was ABSENT is distinguished from one set to empty: a PATCH
// that names no colour must keep the stored one, and a handler that read
// "" as "set the colour to nothing" would erase it.
type rankFields struct {
	// values holds the attributes the body named.
	values map[string]any
}

// name returns the rank's name.
func (f rankFields) name() (string, bool) {
	text, ok := f.stringField("name")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

// color returns the rank's colour.
func (f rankFields) color() (string, bool) {
	text, ok := f.stringField("color")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

// stringField reads a named field as text.
//
// A field the body named but spelled as anything other than a string is an
// ERROR rather than an empty string: a client that posts `"points": {}`
// must be told, not have its rank saved with a blank name.
func (f rankFields) stringField(name string) (string, bool) {
	raw, ok := f.values[name]
	if !ok {
		return "", false
	}

	text, ok := raw.(string)
	if !ok {
		return "", true
	}

	return text, true
}

// colorOr returns the rank's colour, or a fallback when it named none.
func (f rankFields) colorOr(fallback string) string {
	if color, ok := f.color(); ok {
		return color
	}
	return fallback
}

// points returns the rank's threshold.
//
// A JSON number arrives as float64 and a client that spelled it as a
// string arrives as a string; both are accepted, because the admin form
// posts whichever the input holds.
func (f rankFields) points() (int, bool) {
	raw, ok := f.values["points"]
	if !ok {
		return 0, false
	}

	switch typed := raw.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// rankBody decodes a rank request body.
//
// Upstream's client speaks JSON:API (`{"data":{"attributes":{…}}}`) and a
// plain client posts the attributes directly; both are accepted, the same
// way the vote route accepts both.
func rankBody(e *core.RequestEvent) (rankFields, error) {
	body := map[string]any{}
	if err := api.DecodeBody(e, &body); err != nil {
		return rankFields{}, err
	}

	attributes := body
	if data, ok := body["data"].(map[string]any); ok {
		if nested, ok := data["attributes"].(map[string]any); ok {
			attributes = nested
		} else if _, hasName := data["name"]; hasName {
			attributes = data
		}
	}

	return rankFields{values: attributes}, nil
}

// listRanks returns every rank, in ladder order.
func listRanks(app core.App, physical string) ([]rank, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "gamification: the %q collection is missing", physical)
	}

	records, err := app.FindAllRecords(collection, nil)
	if err != nil {
		return nil, errors.Wrap(err, "gamification: the ranks could not be read")
	}

	out := make([]rank, 0, len(records))
	for _, record := range records {
		out = append(out, rankOf(record))
	}

	slices.SortFunc(out, func(a, b rank) int {
		if a.Points != b.Points {
			return a.Points - b.Points
		}
		if compared := strings.Compare(a.Name, b.Name); compared != 0 {
			return compared
		}
		return strings.Compare(a.ID, b.ID)
	})

	return out, nil
}

// findRank returns one rank.
func findRank(app core.App, physical, id string) (rank, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return rank{}, errors.Wrapf(err, "gamification: the %q collection is missing", physical)
	}

	record, err := app.FindRecordById(collection, id)
	if err != nil {
		return rank{}, err
	}

	return rankOf(record), nil
}

// rankOf reads a rank out of a record.
func rankOf(record *core.Record) rank {
	return rank{
		ID:     record.Id,
		Name:   record.GetString("name"),
		Color:  record.GetString("color"),
		Points: record.GetInt("points"),
	}
}

// saveRank writes a rank, creating it when id is empty.
func saveRank(app core.App, physical, id string, r rank) (rank, error) {
	collection, err := app.FindCollectionByNameOrId(physical)
	if err != nil {
		return rank{}, errors.Wrapf(err, "gamification: the %q collection is missing", physical)
	}

	record := core.NewRecord(collection)
	if id != "" {
		record, err = app.FindRecordById(collection, id)
		if err != nil {
			return rank{}, err
		}
	}

	record.Set("name", r.Name)
	record.Set("color", r.Color)
	record.Set("points", r.Points)

	if err := app.Save(record); err != nil {
		return rank{}, errors.Wrap(err, "gamification: the rank could not be saved")
	}

	return rankOf(record), nil
}

// renderRankAt renders a rank for the `included` walk.
//
// It is the extension point's Render func: the forum's include walk has no
// other way to reach a plugin's OWN resource type, and without it a user's
// `ranks` relationship carries the IDENTIFIER and no RESOURCE — so the
// frontend's store never learns the rank's name or colour and the badge
// renders as nothing, with no error anywhere.
func (p *Plugin) renderRankAt(_ context.Context, req *plugin.ResourceRequest) (*plugin.Resource, error) {
	r, err := findRank(req.App, p.physical[ranksCollection], req.ID)
	if err != nil {
		// A rank that is gone is simply absent from the `included`
		// array: a missing relation is not a failed request.
		return nil, nil //nolint:nilerr // a missing resource is not a failed request
	}

	return pluginRankResource(r), nil
}

// errRankMethod reports a POST to the rank route with no method override.
var errRankMethod = errors.New("gamification: the rank route needs PATCH or DELETE (X-HTTP-Method-Override)")
