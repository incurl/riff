import Model from '../Model';
import withIconImage from '../utils/withIconImage';

export default class Group extends Model {
  /**
   * The well-known group ids.
   *
   * Upstream Flarum's ids are the integers 1, 2 and 3, because its
   * `groups` table has an autoincrementing integer primary key and the
   * built-in groups are the first rows. This port's groups live in a
   * PocketBase collection whose record ids are 15-character strings, so
   * the well-known groups carry string ids instead.
   *
   * These constants have to match the ids the BACKEND uses
   * (`pb/flarum/domain/models.go`: `GroupAdminID`, `GroupGuestID` and
   * `GroupMemberID`). The upstream values were left in place by mistake,
   * which silently disabled every comparison against them: the admin UI
   * offered the guest and member groups for assignment (the server
   * rejects them), the "is this account an administrator" check never
   * matched, and the permission dropdown's "everyone" / "members" badges
   * never lit up.
   */
  static ADMINISTRATOR_ID = 'forumgroupadmin';
  static GUEST_ID = 'forumgroupguest';
  static MEMBER_ID = 'forumgroupmembr';

  nameSingular() {
    return Model.attribute<string>('nameSingular').call(this);
  }
  namePlural() {
    return Model.attribute<string>('namePlural').call(this);
  }

  color() {
    return Model.attribute<string | null>('color').call(this);
  }
  /**
   * The Font Awesome icon classes, with the classes that show the uploaded
   * image in their place when the group has one. The stored classes alone are
   * `attribute('icon')`.
   */
  icon() {
    return withIconImage(Model.attribute<string | null>('icon').call(this), this.iconImageUrl(), this.iconImageSrcset());
  }
  iconImageUrl() {
    return Model.attribute<string | null>('iconImageUrl').call(this);
  }
  iconImageSrcset() {
    return Model.attribute<string | null>('iconImageSrcset').call(this);
  }

  isHidden() {
    return Model.attribute<boolean>('isHidden').call(this);
  }

  position() {
    return Model.attribute<number>('position').call(this);
  }
}
