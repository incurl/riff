// The tag control on a discussion page.
//
// Upstream's `addTagControl`: a button in the discussion's controls menu
// that opens the tag picker for an EXISTING discussion and saves the result.
//
// The save is a `PATCH` carrying the `tags` RELATIONSHIP — the same shape
// the composer sends, and the same relationship the server's writer reads.
// The frontend rewrites a PATCH into a POST with an `X-HTTP-Method-Override`
// header, which the server's POST twin resolves (see
// `api.EffectiveMethod`).
import app from 'flarum/forum/app';
import { extend as extendMethod } from 'flarum/common/extend';
import DiscussionControls from 'flarum/forum/utils/DiscussionControls';
import Button from 'flarum/common/components/Button';

import TagSelectionModal from './components/TagSelectionModal';

/**
 * Open the picker for a discussion and save the chosen tags.
 *
 * The failure is shown rather than swallowed: a tag set the server refuses
 * (the count rules, or the `allow_tag_change` setting) is a message the
 * author needs, and a silent failure would leave the modal closed and the
 * tags unchanged with nothing said.
 *
 * @param {object} discussion
 */
export function showTagControl(discussion) {
  app.modal.show(TagSelectionModal, {
    // The discussion's CURRENT tags are the starting selection.
    selectedTags: (discussion.tags() || []).slice(0),
    title: 'Edit tags',
    onsubmit: (tags) => {
      discussion.save({ relationships: { tags } }).catch((error) => {
        app.modal.show(TagSelectionModal, {
          selectedTags: (discussion.tags() || []).slice(0),
          title: 'Edit tags',
          onsubmit: (chosen) => {
            discussion.save({ relationships: { tags: chosen } }).catch(() => {});
          },
        });

        throw error;
      });
    },
  });
}

export default function addTagControl() {
  extendMethod(DiscussionControls, 'moderationControls', function (items, discussion) {
    // The flag is the SERVER's answer (`canTag`), computed by the same
    // policy the write path uses — so the control is offered exactly when
    // the save would be accepted.
    if (discussion.canTag()) {
      items.add(
        'tags',
        <Button icon="fas fa-tag" onclick={() => showTagControl(discussion)}>
          Edit tags
        </Button>
      );
    }
  });
}
