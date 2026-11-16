import type Mithril from 'mithril';
import Component, { ComponentAttrs } from '../Component';
import classList from '../utils/classList';
import Icon from './Icon';

export interface IColorPreviewInputAttrs extends ComponentAttrs {
  value: string;
  id?: string;
  type?: string;
  onchange?: (event: { target: { value: string } }) => void;
}

export default class ColorPreviewInput<
  CustomAttributes extends IColorPreviewInputAttrs = IColorPreviewInputAttrs,
> extends Component<CustomAttributes> {
  view(vnode: Mithril.Vnode<CustomAttributes, this>) {
    const { className, id, ...otherAttrs } = this.attrs;

    const attrs = otherAttrs as unknown as IColorPreviewInputAttrs;

    attrs.type ||= 'text';

    attrs.onblur = () => {
      if (attrs.value.length === 4) {
        attrs.value = attrs.value.replace(/#([a-f0-9])([a-f0-9])([a-f0-9])/, '#$1$1$2$2$3$3');
        attrs.onchange?.({ target: { value: attrs.value } });
      }

      // An empty field is a valid "no colour" state and must be preserved as
      // such (stored as an empty string / null), not coerced to a colour. Only
      // a non-empty value that fails validation is corrected.
      if (attrs.value !== '' && !/^#[a-f0-9]{6}$/i.test(attrs.value)) {
        attrs.value = '#000000';
        attrs.onchange?.({ target: { value: attrs.value } });
      }
    };

    // The preview is an <input type="color">, and that element's value
    // sanitization algorithm is "value = a valid CSS color". An EMPTY
    // value is not a valid CSS color, so assigning one throws:
    //
    //   The specified value "" does not conform to the required format.
    //   The value must be a valid CSS color.
    //
    // An empty colour is a legitimate state here — it means "no colour
    // set" — and the TEXT input keeps it. The preview swatch cannot, so
    // it falls back to a neutral grey: the swatch shows what picking a
    // colour would do, not the (absent) saved value.
    const previewAttrs = { ...attrs };
    previewAttrs.value = attrs.value && /^#[a-f0-9]{6}$/i.test(attrs.value) ? attrs.value : '#cccccc';

    return (
      <div className="ColorInput">
        <input className={classList('FormControl', className)} id={id} {...attrs} />

        <span className="ColorInput-icon" role="presentation">
          <Icon name={'fas fa-exclamation-circle'} />
        </span>

        <input className="ColorInput-preview" {...previewAttrs} type="color" />
      </div>
    );
  }
}
