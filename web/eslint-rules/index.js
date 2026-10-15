import selectValueLabel from "./select-value-label.js";
import noRawSelect from "./no-raw-select.js";
import noSecureContextCrypto from "./no-secure-context-crypto.js";

export default {
  rules: {
    "select-value-label": selectValueLabel,
    "no-raw-select": noRawSelect,
    "no-secure-context-crypto": noSecureContextCrypto,
  },
};
