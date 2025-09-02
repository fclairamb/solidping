We want to introduce the concept of subchecks. They are check that have a parent check.

For example, a HTTP check will enable subcheck for domain expiration that will be only executed once per day.

It shall be a simple option to enable in the http check and will end up creating a new domain_expiration check.
