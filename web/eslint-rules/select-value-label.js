// <SelectValue /> 没有子内容且所在 <Select> 没传 items 时，收起态会直接显示内部
// value（Base UI 行为），例如把「否（默认）」渲染成 "no"。本规则拦截这种写法：
// 要么给 Select 传 items，要么给 SelectValue 写子内容。业务代码应直接用 OptionSelect。
// 注意：动态子内容（{labelOf(v)}、{cond && label}）静态无法求证运行时是否为空，
// 规则默认信任；能静态确定为空的（{undefined}、{null}、{false}、{""}）照常拦截。
export default {
  meta: {
    type: "problem",
    docs: {
      description:
        "SelectValue 无子内容时，所属 Select 必须传 items 才能显示选项标签",
    },
    messages: {
      missingLabel:
        "SelectValue 没有子内容且所属 Select 没有传 items：收起时会直接显示内部 value。请改用 OptionSelect，或给 Select 传 items / 给 SelectValue 写标签。",
    },
    schema: [],
  },
  create(context) {
    // 支持 <Select> / <SelectValue> 和 <Select.Root> / <Select.Value> 两种写法
    //（成员写法常见于 ui/ 封装层，不识别会让封装内部误报/漏报）。
    function elementName(element) {
      const name = element.openingElement.name;
      if (name.type === "JSXIdentifier") return name.name;
      if (
        name.type === "JSXMemberExpression" &&
        name.object.type === "JSXIdentifier" &&
        (name.object.name === "Select" || name.object.name === "SelectPrimitive") &&
        name.property.type === "JSXIdentifier"
      ) {
        if (name.property.name === "Root") return "Select";
        if (name.property.name === "Value") return "SelectValue";
      }
      return null;
    }

    function isEmptyExpression(expr) {
      if (expr.type === "JSXEmptyExpression") return true;
      if (expr.type === "Identifier") return expr.name === "undefined";
      if (expr.type === "Literal") {
        return (
          expr.value === null ||
          expr.value === false ||
          expr.value === true ||
          expr.value === ""
        );
      }
      return false;
    }

    function hasMeaningfulChildren(element) {
      // {...props} 展开可能携带 children（ui/ 封装层的透传写法），按有子内容处理。
      if (
        element.openingElement.attributes.some(
          (attr) => attr.type === "JSXSpreadAttribute",
        )
      ) {
        return true;
      }
      const childrenAttr = element.openingElement.attributes.find(
        (attr) => attr.type === "JSXAttribute" && attr.name.name === "children",
      );
      if (childrenAttr) {
        const v = childrenAttr.value;
        if (v == null) return false;
        if (v.type === "JSXExpressionContainer") {
          return !isEmptyExpression(v.expression);
        }
        return true;
      }
      return element.children.some(
        (child) =>
          (child.type === "JSXText" && child.value.trim() !== "") ||
          (child.type === "JSXExpressionContainer" &&
            !isEmptyExpression(child.expression)) ||
          child.type === "JSXElement" ||
          child.type === "JSXFragment",
      );
    }

    // 只认显式 items= 属性；{...props} 这类展开无法静态确认含 items，不算数。
    function passesItems(selectElement) {
      return selectElement.openingElement.attributes.some(
        (attr) => attr.type === "JSXAttribute" && attr.name.name === "items",
      );
    }

    return {
      JSXElement(node) {
        if (elementName(node) !== "SelectValue" || hasMeaningfulChildren(node)) {
          return;
        }
        let ancestor = node.parent;
        while (ancestor) {
          if (
            ancestor.type === "JSXElement" &&
            elementName(ancestor) === "Select"
          ) {
            break;
          }
          ancestor = ancestor.parent;
        }
        const select =
          ancestor && ancestor.type === "JSXElement" ? ancestor : null;
        if (!select || !passesItems(select)) {
          context.report({ node: node.openingElement, messageId: "missingLabel" });
        }
      },
    };
  },
};
