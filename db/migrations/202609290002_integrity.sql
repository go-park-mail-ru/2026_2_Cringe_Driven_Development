-- +goose Up
SET LOCAL search_path TO colab, public;

-- Общая служебная проверка дат и идентичности строк.
-- Владелец проекта неизменяем: история запусков и квота принадлежат ему.
-- +goose StatementBegin
CREATE FUNCTION colab.touch_row() RETURNS trigger
LANGUAGE plpgsql SET search_path = colab, pg_temp AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'id and created_at are immutable' USING ERRCODE = '23514';
    END IF;
    IF TG_TABLE_NAME = 'project' THEN
        IF NEW.user_id <> OLD.user_id THEN
            RAISE EXCEPTION 'project owner is immutable; copy the project' USING ERRCODE = '23514';
        END IF;
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    relation_name text;
BEGIN
    FOR relation_name IN
        SELECT table_name
          FROM information_schema.tables
         WHERE table_schema = 'colab'
           AND table_type = 'BASE TABLE'
    LOOP
        EXECUTE format(
            'CREATE TRIGGER touch_row BEFORE UPDATE ON colab.%I '
            'FOR EACH ROW EXECUTE FUNCTION colab.touch_row()', relation_name
        );
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- Межтабличное условие нельзя выразить обычным CHECK.
-- FOR SHARE исключает изменение условий тарифа одновременно с оформлением подписки.
-- +goose StatementBegin
CREATE FUNCTION colab.check_subscription_type() RETURNS trigger
LANGUAGE plpgsql SET search_path = colab, pg_temp AS $$
DECLARE
    type_price numeric(12, 2);
BEGIN
    SELECT price_rub INTO type_price
      FROM subscription_type
     WHERE id = NEW.subscription_type_id
       FOR SHARE;
    IF type_price = 0 THEN
        RAISE EXCEPTION 'free access does not require a user_subscription' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER check_subscription_type BEFORE INSERT OR UPDATE ON user_subscription
    FOR EACH ROW EXECUTE FUNCTION check_subscription_type();

-- Исторические условия покупки не меняются задним числом.
-- Бесплатный тариф фиксирован: его применение не создаёт строк user_subscription.
-- +goose StatementBegin
CREATE FUNCTION colab.protect_subscription_type() RETURNS trigger
LANGUAGE plpgsql SET search_path = colab, pg_temp AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.price_rub = 0 THEN
            RAISE EXCEPTION 'default subscription type cannot be deleted' USING ERRCODE = '23514';
        END IF;
        RETURN OLD;
    END IF;
    IF (NEW.runtime_limit, NEW.ram_limit, NEW.cpu_limit, NEW.price_rub, NEW.duration)
       IS DISTINCT FROM
       (OLD.runtime_limit, OLD.ram_limit, OLD.cpu_limit, OLD.price_rub, OLD.duration)
       AND (OLD.price_rub = 0 OR EXISTS (
           SELECT 1
             FROM user_subscription
            WHERE subscription_type_id = OLD.id
       )) THEN
        RAISE EXCEPTION 'used or default subscription terms are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER protect_subscription_type BEFORE UPDATE OR DELETE ON subscription_type
    FOR EACH ROW EXECUTE FUNCTION protect_subscription_type();

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    trigger_row record;
BEGIN
    FOR trigger_row IN
        SELECT t.tgname, c.relname
          FROM pg_trigger AS t
          JOIN pg_class AS c ON c.oid = t.tgrelid
          JOIN pg_namespace AS n ON n.oid = c.relnamespace
         WHERE n.nspname = 'colab'
           AND NOT t.tgisinternal
    LOOP
        EXECUTE format('DROP TRIGGER %I ON colab.%I', trigger_row.tgname, trigger_row.relname);
    END LOOP;
END;
$$;
-- +goose StatementEnd
DROP FUNCTION colab.touch_row(), colab.check_subscription_type(), colab.protect_subscription_type();
