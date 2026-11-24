// The Mentions extension's forum initialiser.
//
// The server already does the work: a post that says `@alice` stores
// the mention, notifies Alice, and the post resource carries
// `mentionedByCount` and the `mentionedBy` relationship.
//
// What the front end adds is the NOTIFICATION COMPONENT. The SPA looks
// a notification's component up by its `contentType`
// (`NotificationType.tsx` reads `notification.contentType()` and uses
// it as the key into `app.notificationComponents`), so a kind with no
// registered component renders an EMPTY ROW — the unread badge still
// counts it, because the badge reads `isRead`.
//
// The server sends the kind as `contentType`, so registering the
// component under that name is what makes the row appear.
//
// NOTHING may touch `app.forum` or `app.session` at module scope: the
// shell runs `app.load(payload)`, then `app.bootExtensions(...)`, and
// only then `app.boot()` — and `app.boot()` is what assigns them.
// `app.request` reads `this.session.csrfToken`, so a request fired
// from an extender would throw on an undefined session.
//
// The extender only registers a component and a model method, and the
// requests happen later, on a click — long after the app is booted.
import app from 'flarum/forum/app';
import Extend from 'flarum/common/extenders';
import Notification from 'flarum/forum/components/Notification';
import NotificationModel from 'flarum/common/models/Notification';
import Post from 'flarum/common/models/Post';

/**
 * The `UserMentionedNotification` component displays a notification
 * which indicates that the reader was mentioned in a post.
 */
class UserMentionedNotification extends Notification {
  icon() {
    return 'fas fa-at';
  }

  href() {
    const notification = this.attrs.notification;
    const post = notification.post();

    if (!post) {
      return '#';
    }

    return app.route.post(post);
  }

  content() {
    return app.translator.trans('core.forum.notifications.mentioned_text', { user: this.attrs.notification.fromUser() });
  }

  excerpt() {
    const notification = this.attrs.notification;
    const content = notification.content();

    return content && content.postNumber ? app.translator.trans('core.forum.notifications.mentioned_excerpt', { postNumber: content.postNumber }) : null;
  }
}

export const extend = [
  // The notification's `post` relationship.
  //
  // The server sends it, and the notification component links to the
  // post — but the core's Notification model declares no accessor for
  // it, so `notification.post()` does not exist without this.
  //
  // It has to be registered as a MODEL METHOD: the server sends it as
  // a relationship, and `notification.post()` is a method call.
  new Extend.Model(NotificationModel).hasOne('post'),

  // The post's own mention attributes.
  //
  // The same reason: the server sends them as plain attributes, and
  // `post.mentionedByCount()` is a method call — without the
  // registration the method does not exist on the model and the render
  // throws.
  new Extend.Model(Post).hasMany('mentionedBy').attribute('mentionedByCount'),

  // The notification component for the mention kind.
  //
  // The name is the kind the SERVER sends: the notifications
  // collection's `type` field carries `userMentioned` (upstream's
  // `UserMentionedBlueprint::getType()`), and the serializer publishes
  // it as `contentType`.
  new Extend.Notification().add('userMentioned', UserMentionedNotification),
];
