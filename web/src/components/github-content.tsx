import ReactMarkdown from "react-markdown";
import rehypeRaw from "rehype-raw";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";

const sanitizeSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    img: [...(defaultSchema.attributes?.img ?? []), "width", "height"],
  },
};

interface GitHubContentProps {
  html?: string | null;
  markdown?: string | null;
  className?: string;
}

export function GitHubContent({
  html,
  markdown,
  className,
}: GitHubContentProps) {
  if (html?.trim()) {
    return (
      <div
        className={className}
        // body_html comes from GitHub's rendered HTML response, not raw author HTML.
        dangerouslySetInnerHTML={{ __html: html }}
      />
    );
  }

  if (markdown?.trim()) {
    return (
      <div className={className}>
        <ReactMarkdown
          rehypePlugins={[rehypeRaw, [rehypeSanitize, sanitizeSchema]]}
        >
          {markdown}
        </ReactMarkdown>
      </div>
    );
  }

  return null;
}
