// <SelectValue /> 没有子内容且所在 <Select> 没传 items 时，收起态会直接显示内部
// value（Base UI 行为），例如把「否（默认）」渲染成 "no"。本规则拦截这种写法：
// 要么给 Select 传 items，要么给 SelectValue 写子内容。业务代码应直接用 OptionSelect。
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
    function elementName(element) {
      const name = element.openingElement.name;
      return name.type === "JSXIdentifier" ? name.name : null;
    }

    function hasMeaningfulChildren(element) {
      return (
        element.openingElement.attributes.some(
          (attr) =>
            attr.type === "JSXAttribute" && attr.name.name === "children",
        ) ||
        element.children.some(
          (child) =>
            (child.type === "JSXText" && child.value.trim() !== "") ||
            (child.type === "JSXExpressionContainer" &&
              child.expression.type !== "JSXEmptyExpression") ||
            child.type === "JSXElement" ||
            child.type === "JSXFragment",
        )
      );
    }

    function passesItems(selectElement) {
      return selectElement.openingElement.attributes.some(
        (attr) =>
          attr.type === "JSXSpreadAttribute" ||
          (attr.type === "JSXAttribute" && attr.name.name === "items"),
      );
    }

    return {
      JSXElement(node) {
        if (elementName(node) !== "SelectValue" || hasMeaningfulChildren(node)) {
          return;
        }
        let ancestor = node.parent;
        while (ancestor) {
          if (ancestor.type === "JSXElement" || ancestor.type === "JSXFragment") {
            if (
              ancestor.type === "JSXElement" &&
              elementName(ancestor) === "Select"
            ) {
              break;
            }
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
